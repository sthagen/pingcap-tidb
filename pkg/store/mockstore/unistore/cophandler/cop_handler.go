// Copyright 2020 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cophandler

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/pingcap/errors"
	"github.com/pingcap/failpoint"
	"github.com/pingcap/kvproto/pkg/coprocessor"
	"github.com/pingcap/kvproto/pkg/kvrpcpb"
	"github.com/pingcap/tidb/pkg/expression"
	"github.com/pingcap/tidb/pkg/expression/aggregation"
	"github.com/pingcap/tidb/pkg/kv"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser/charset"
	"github.com/pingcap/tidb/pkg/parser/mysql"
	"github.com/pingcap/tidb/pkg/parser/terror"
	"github.com/pingcap/tidb/pkg/sessionctx"
	"github.com/pingcap/tidb/pkg/sessionctx/stmtctx"
	"github.com/pingcap/tidb/pkg/sessionctx/vardef"
	"github.com/pingcap/tidb/pkg/store/mockstore/unistore/client"
	"github.com/pingcap/tidb/pkg/store/mockstore/unistore/lockstore"
	"github.com/pingcap/tidb/pkg/store/mockstore/unistore/tikv/dbreader"
	"github.com/pingcap/tidb/pkg/store/mockstore/unistore/tikv/kverrors"
	"github.com/pingcap/tidb/pkg/types"
	"github.com/pingcap/tidb/pkg/util/chunk"
	"github.com/pingcap/tidb/pkg/util/codec"
	"github.com/pingcap/tidb/pkg/util/collate"
	contextutil "github.com/pingcap/tidb/pkg/util/context"
	"github.com/pingcap/tidb/pkg/util/mock"
	"github.com/pingcap/tidb/pkg/util/rowcodec"
	"github.com/pingcap/tipb/go-tipb"
)

var globalLocationMap *locationMap = newLocationMap()

type locationMap struct {
	lmap map[string]*time.Location
	mu   sync.RWMutex
}

func newLocationMap() *locationMap {
	return &locationMap{
		lmap: make(map[string]*time.Location),
	}
}

func (l *locationMap) getLocation(name string) (*time.Location, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	result, ok := l.lmap[name]
	return result, ok
}

func (l *locationMap) setLocation(name string, value *time.Location) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lmap[name] = value
}

// MPPCtx is the mpp execution context
type MPPCtx struct {
	RPCClient   client.Client
	StoreAddr   string
	TaskHandler *MPPTaskHandler
	Ctx         context.Context
}

// HandleCopRequest handles coprocessor request.
func HandleCopRequest(dbReader *dbreader.DBReader, lockStore *lockstore.MemStore, req *coprocessor.Request) *coprocessor.Response {
	return HandleCopRequestWithMPPCtx(dbReader, lockStore, req, nil)
}

// HandleCopRequestWithMPPCtx handles coprocessor request, actually, this is the updated version for
// HandleCopRequest(after mpp test is supported), however, go does not support function overloading,
// I have to rename it to HandleCopRequestWithMPPCtx.
func HandleCopRequestWithMPPCtx(dbReader *dbreader.DBReader, lockStore *lockstore.MemStore, req *coprocessor.Request, mppCtx *MPPCtx) *coprocessor.Response {
	switch req.Tp {
	case kv.ReqTypeDAG:
		if mppCtx != nil && mppCtx.TaskHandler != nil {
			return HandleMPPDAGReq(dbReader, req, mppCtx)
		}
		return handleCopDAGRequest(dbReader, lockStore, req)
	case kv.ReqTypeAnalyze:
		return handleCopAnalyzeRequest(dbReader, req)
	case kv.ReqTypeChecksum:
		return handleCopChecksumRequest(dbReader, req)
	}
	return &coprocessor.Response{OtherError: fmt.Sprintf("unsupported request type %d", req.GetTp())}
}

type dagContext struct {
	*evalContext
	dbReader      *dbreader.DBReader
	lockStore     *lockstore.MemStore
	resolvedLocks []uint64
	dagReq        *tipb.DAGRequest
	keyRanges     []*coprocessor.KeyRange
	startTS       uint64
}

// ExecutorListsToTree converts a list of executors to a tree.
func ExecutorListsToTree(exec []*tipb.Executor) *tipb.Executor {
	i := len(exec) - 1
	rootExec := exec[i]
	for i--; 0 <= i; i-- {
		switch exec[i+1].Tp {
		case tipb.ExecType_TypeAggregation:
			exec[i+1].Aggregation.Child = exec[i]
		case tipb.ExecType_TypeProjection:
			exec[i+1].Projection.Child = exec[i]
		case tipb.ExecType_TypeTopN:
			exec[i+1].TopN.Child = exec[i]
		case tipb.ExecType_TypeLimit:
			exec[i+1].Limit.Child = exec[i]
		case tipb.ExecType_TypeSelection:
			exec[i+1].Selection.Child = exec[i]
		case tipb.ExecType_TypeStreamAgg:
			exec[i+1].Aggregation.Child = exec[i]
		default:
			panic("unsupported dag executor type")
		}
	}
	return rootExec
}

// handleCopDAGRequest handles coprocessor DAG request using MPP executors.
func handleCopDAGRequest(dbReader *dbreader.DBReader, lockStore *lockstore.MemStore, req *coprocessor.Request) (resp *coprocessor.Response) {
	startTime := time.Now()
	resp = &coprocessor.Response{}
	failpoint.Inject("mockCopCacheInUnistore", func(cacheVersion failpoint.Value) {
		if req.IsCacheEnabled {
			if uint64(cacheVersion.(int)) == req.CacheIfMatchVersion {
				failpoint.Return(&coprocessor.Response{IsCacheHit: true, CacheLastVersion: uint64(cacheVersion.(int))})
			} else {
				defer func() {
					resp.CanBeCached = true
					resp.CacheLastVersion = uint64(cacheVersion.(int))
					if resp.ExecDetails == nil {
						resp.ExecDetails = &kvrpcpb.ExecDetails{TimeDetail: &kvrpcpb.TimeDetail{ProcessWallTimeMs: 500}}
					} else if resp.ExecDetails.TimeDetail == nil {
						resp.ExecDetails.TimeDetail = &kvrpcpb.TimeDetail{ProcessWallTimeMs: 500}
					} else {
						resp.ExecDetails.TimeDetail.ProcessWallTimeMs = 500
					}
				}()
			}
		}
	})
	dagCtx, dagReq, err := buildDAG(dbReader, lockStore, req)
	if err != nil {
		resp.OtherError = err.Error()
		return resp
	}

	exec, chunks, lastRange, counts, ndvs, err := buildAndRunMPPExecutor(dagCtx, dagReq, req.PagingSize)

	sc := dagCtx.sctx.GetSessionVars().StmtCtx
	if err != nil {
		errMsg := err.Error()
		if strings.HasPrefix(errMsg, ErrExecutorNotSupportedMsg) {
			resp.OtherError = err.Error()
			return resp
		}
		return genRespWithMPPExec(nil, lastRange, nil, nil, exec, dagReq, err, sc.GetWarnings(), time.Since(startTime))
	}
	return genRespWithMPPExec(chunks, lastRange, counts, ndvs, exec, dagReq, err, sc.GetWarnings(), time.Since(startTime))
}

func buildAndRunMPPExecutor(dagCtx *dagContext, dagReq *tipb.DAGRequest, pagingSize uint64) (mppExec, []tipb.Chunk, *coprocessor.KeyRange, []int64, []int64, error) {
	rootExec := dagReq.RootExecutor
	if rootExec == nil {
		rootExec = ExecutorListsToTree(dagReq.Executors)
	}

	var counts, ndvs []int64

	if dagReq.GetCollectRangeCounts() {
		counts = make([]int64, len(dagCtx.keyRanges))
		ndvs = make([]int64, len(dagCtx.keyRanges))
	}
	builder := &mppExecBuilder{
		sctx:     dagCtx.sctx,
		dbReader: dagCtx.dbReader,
		dagReq:   dagReq,
		dagCtx:   dagCtx,
		mppCtx:   nil,
		counts:   counts,
		ndvs:     ndvs,
	}
	var lastRange *coprocessor.KeyRange
	if pagingSize > 0 {
		lastRange = &coprocessor.KeyRange{}
		builder.paging = lastRange
		builder.pagingSize = pagingSize
	}
	exec, err := builder.buildMPPExecutor(rootExec)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	chunks, err := mppExecute(exec, dagCtx, dagReq, pagingSize)
	if lastRange != nil && len(lastRange.Start) == 0 && len(lastRange.End) == 0 {
		// When should this happen, something is wrong?
		lastRange = nil
	}
	return exec, chunks, lastRange, counts, ndvs, err
}

func mppExecute(exec mppExec, dagCtx *dagContext, dagReq *tipb.DAGRequest, pagingSize uint64) (chunks []tipb.Chunk, err error) {
	err = exec.open()
	defer func() {
		err := exec.stop()
		if err != nil {
			panic(err)
		}
	}()
	if err != nil {
		return
	}

	var totalRows uint64
	var chk *chunk.Chunk
	fields := exec.getFieldTypes()
	for {
		chk, err = exec.next()
		if err != nil || chk == nil || chk.NumRows() == 0 {
			return
		}

		switch dagReq.EncodeType {
		case tipb.EncodeType_TypeDefault:
			chunks, err = useDefaultEncoding(chk, dagCtx, dagReq, fields, chunks)
		case tipb.EncodeType_TypeChunk:
			chunks = useChunkEncoding(chk, dagReq, fields, chunks)
			if pagingSize > 0 {
				totalRows += uint64(chk.NumRows())
				if totalRows > pagingSize {
					return
				}
			}
		default:
			err = fmt.Errorf("unsupported DAG request encode type %s", dagReq.EncodeType)
		}
		if err != nil {
			return
		}
	}
}

func useDefaultEncoding(chk *chunk.Chunk, dagCtx *dagContext, dagReq *tipb.DAGRequest,
	fields []*types.FieldType, chunks []tipb.Chunk) ([]tipb.Chunk, error) {
	var buf []byte
	var datums []types.Datum
	var err error
	numRows := chk.NumRows()
	sc := dagCtx.sctx.GetSessionVars().StmtCtx
	errCtx := sc.ErrCtx()
	for i := range numRows {
		datums = datums[:0]
		if dagReq.OutputOffsets != nil {
			for _, j := range dagReq.OutputOffsets {
				datums = append(datums, chk.GetRow(i).GetDatum(int(j), fields[j]))
			}
		} else {
			for j, ft := range fields {
				datums = append(datums, chk.GetRow(i).GetDatum(j, ft))
			}
		}
		buf, err = codec.EncodeValue(sc.TimeZone(), buf[:0], datums...)
		err = errCtx.HandleError(err)
		if err != nil {
			return nil, errors.Trace(err)
		}
		chunks = appendRow(chunks, buf, i)
	}
	return chunks, nil
}

func useChunkEncoding(chk *chunk.Chunk, dagReq *tipb.DAGRequest, fields []*types.FieldType, chunks []tipb.Chunk) []tipb.Chunk {
	if dagReq.OutputOffsets != nil {
		offsets := make([]int, len(dagReq.OutputOffsets))
		newFields := make([]*types.FieldType, len(dagReq.OutputOffsets))
		for i := range dagReq.OutputOffsets {
			offset := dagReq.OutputOffsets[i]
			offsets[i] = int(offset)
			newFields[i] = fields[offset]
		}
		chk = chk.Prune(offsets)
		fields = newFields
	}

	c := chunk.NewCodec(fields)
	buffer := c.Encode(chk)
	chunks = append(chunks, tipb.Chunk{
		RowsData: buffer,
	})
	return chunks
}

func buildDAG(reader *dbreader.DBReader, lockStore *lockstore.MemStore, req *coprocessor.Request) (*dagContext, *tipb.DAGRequest, error) {
	if len(req.Ranges) == 0 {
		return nil, nil, errors.New("request range is null")
	}
	if req.GetTp() != kv.ReqTypeDAG {
		return nil, nil, errors.Errorf("unsupported request type %d", req.GetTp())
	}

	dagReq := new(tipb.DAGRequest)
	err := proto.Unmarshal(req.Data, dagReq)
	if err != nil {
		return nil, nil, errors.Trace(err)
	}
	var tz *time.Location
	switch dagReq.TimeZoneName {
	case "":
		tz = time.FixedZone("UTC", int(dagReq.TimeZoneOffset))
	case "System":
		tz = time.Local
	default:
		var ok bool
		tz, ok = globalLocationMap.getLocation(dagReq.TimeZoneName)
		if !ok {
			tz, err = time.LoadLocation(dagReq.TimeZoneName)
			if err != nil {
				return nil, nil, errors.Trace(err)
			}
			globalLocationMap.setLocation(dagReq.TimeZoneName, tz)
		}
	}
	sctx := flagsAndTzToSessionContext(dagReq.Flags, tz)
	if dagReq.DivPrecisionIncrement != nil {
		sctx.GetSessionVars().DivPrecisionIncrement = int(*dagReq.DivPrecisionIncrement)
	} else {
		sctx.GetSessionVars().DivPrecisionIncrement = vardef.DefDivPrecisionIncrement
	}
	ctx := &dagContext{
		evalContext:   &evalContext{sctx: sctx},
		dbReader:      reader,
		lockStore:     lockStore,
		dagReq:        dagReq,
		keyRanges:     req.Ranges,
		startTS:       req.StartTs,
		resolvedLocks: req.Context.ResolvedLocks,
	}
	return ctx, dagReq, err
}

func getAggInfo(ctx *dagContext, pbAgg *tipb.Aggregation) ([]aggregation.Aggregation, []expression.Expression, error) {
	length := len(pbAgg.AggFunc)
	aggs := make([]aggregation.Aggregation, 0, length)
	var err error
	for _, expr := range pbAgg.AggFunc {
		var aggExpr aggregation.Aggregation
		aggExpr, _, err = aggregation.NewDistAggFunc(expr, ctx.fieldTps, ctx.sctx.GetExprCtx())
		if err != nil {
			return nil, nil, errors.Trace(err)
		}
		aggs = append(aggs, aggExpr)
	}
	groupBys, err := convertToExprs(ctx.sctx, ctx.fieldTps, pbAgg.GetGroupBy())
	if err != nil {
		return nil, nil, errors.Trace(err)
	}

	return aggs, groupBys, nil
}

func getTopNInfo(ctx *evalContext, topN *tipb.TopN) (heap *topNHeap, conds []expression.Expression, err error) {
	pbConds := make([]*tipb.Expr, len(topN.OrderBy))
	for i, item := range topN.OrderBy {
		pbConds[i] = item.Expr
	}
	heap = &topNHeap{
		totalCount: int(topN.Limit),
		topNSorter: topNSorter{
			orderByItems: topN.OrderBy,
			sc:           ctx.sctx.GetSessionVars().StmtCtx,
		},
	}
	if conds, err = convertToExprs(ctx.sctx, ctx.fieldTps, pbConds); err != nil {
		return nil, nil, errors.Trace(err)
	}

	return heap, conds, nil
}

type evalContext struct {
	columnInfos []*tipb.ColumnInfo
	fieldTps    []*types.FieldType
	primaryCols []int64
	sctx        sessionctx.Context
}

func (e *evalContext) setColumnInfo(cols []*tipb.ColumnInfo) {
	e.columnInfos = make([]*tipb.ColumnInfo, len(cols))
	copy(e.columnInfos, cols)

	e.fieldTps = make([]*types.FieldType, 0, len(e.columnInfos))
	for _, col := range e.columnInfos {
		ft := fieldTypeFromPBColumn(col)
		e.fieldTps = append(e.fieldTps, ft)
	}
}

func newRowDecoder(columnInfos []*tipb.ColumnInfo, fieldTps []*types.FieldType, primaryCols []int64, timeZone *time.Location) (*rowcodec.ChunkDecoder, error) {
	var (
		pkCols []int64
		cols   = make([]rowcodec.ColInfo, 0, len(columnInfos))
	)
	for i := range columnInfos {
		info := columnInfos[i]
		if info.ColumnId == model.ExtraPhysTblID {
			// Skip since it needs to be filled in from the key
			continue
		}
		ft := fieldTps[i]
		col := rowcodec.ColInfo{
			ID:         info.ColumnId,
			Ft:         ft,
			IsPKHandle: info.PkHandle,
		}
		cols = append(cols, col)
		if info.PkHandle {
			pkCols = append(pkCols, info.ColumnId)
		}
	}
	if len(pkCols) == 0 {
		if primaryCols != nil {
			pkCols = primaryCols
		} else {
			pkCols = []int64{-1}
		}
	}
	def := func(i int, chk *chunk.Chunk) error {
		info := columnInfos[i]
		if info.PkHandle || len(info.DefaultVal) == 0 {
			chk.AppendNull(i)
			return nil
		}
		decoder := codec.NewDecoder(chk, timeZone)
		_, err := decoder.DecodeOne(info.DefaultVal, i, fieldTps[i])
		if err != nil {
			return err
		}
		return nil
	}
	return rowcodec.NewChunkDecoder(cols, pkCols, def, timeZone), nil
}

// flagsAndTzToSessionContext creates a sessionctx.Context from a `tipb.SelectRequest.Flags`.
func flagsAndTzToSessionContext(flags uint64, tz *time.Location) sessionctx.Context {
	sc := stmtctx.NewStmtCtx()
	sc.InitFromPBFlagAndTz(flags, tz)
	sctx := mock.NewContextDeprecated()
	sctx.GetSessionVars().StmtCtx = sc
	sctx.GetSessionVars().TimeZone = tz
	return sctx
}

// ErrLocked is returned when trying to Read/Write on a locked key. Client should
// backoff or cleanup the lock then retry.
type ErrLocked struct {
	Key      []byte
	Primary  []byte
	StartTS  uint64
	TTL      uint64
	LockType uint8
}

// BuildLockErr generates ErrKeyLocked objects
func BuildLockErr(key []byte, primaryKey []byte, startTS uint64, TTL uint64, lockType uint8) *ErrLocked {
	errLocked := &ErrLocked{
		Key:      key,
		Primary:  primaryKey,
		StartTS:  startTS,
		TTL:      TTL,
		LockType: lockType,
	}
	return errLocked
}

// Error formats the lock to a string.
func (e *ErrLocked) Error() string {
	return fmt.Sprintf("key is locked, key: %q, Type: %v, primary: %q, startTS: %v", e.Key, e.LockType, e.Primary, e.StartTS)
}

func genRespWithMPPExec(chunks []tipb.Chunk, lastRange *coprocessor.KeyRange, counts, ndvs []int64, exec mppExec, dagReq *tipb.DAGRequest, err error, warnings []contextutil.SQLWarn, dur time.Duration) *coprocessor.Response {
	resp := &coprocessor.Response{
		Range: lastRange,
	}
	selResp := &tipb.SelectResponse{
		Error:        toPBError(err),
		Chunks:       chunks,
		OutputCounts: counts,
		Ndvs:         ndvs,
		EncodeType:   dagReq.EncodeType,
	}
	executors := dagReq.Executors
	if dagReq.GetCollectExecutionSummaries() {
		// for simplicity, we assume all executors to be spending the same amount of time as the request
		timeProcessed := uint64(dur / time.Nanosecond)
		execSummary := make([]*tipb.ExecutorExecutionSummary, len(executors))
		e := exec
		for i := len(executors) - 1; 0 <= i; i-- {
			execSummary[i] = e.buildSummary()
			execSummary[i].TimeProcessedNs = &timeProcessed
			if i != 0 {
				e = exec.child()
			}
		}
		selResp.ExecutionSummaries = execSummary
	}
	if len(warnings) > 0 {
		selResp.Warnings = make([]*tipb.Error, 0, len(warnings))
		for i := range warnings {
			selResp.Warnings = append(selResp.Warnings, toPBError(warnings[i].Err))
		}
	}
	if locked, ok := errors.Cause(err).(*ErrLocked); ok {
		resp.Locked = &kvrpcpb.LockInfo{
			Key:         locked.Key,
			PrimaryLock: locked.Primary,
			LockVersion: locked.StartTS,
			LockTtl:     locked.TTL,
		}
	}
	resp.ExecDetails = &kvrpcpb.ExecDetails{
		TimeDetail: &kvrpcpb.TimeDetail{ProcessWallTimeMs: uint64(dur / time.Millisecond)},
	}
	resp.ExecDetailsV2 = &kvrpcpb.ExecDetailsV2{
		TimeDetail: resp.ExecDetails.TimeDetail,
	}
	data, mErr := proto.Marshal(selResp)
	if mErr != nil {
		resp.OtherError = mErr.Error()
		return resp
	}
	resp.Data = data
	if err != nil {
		if conflictErr, ok := errors.Cause(err).(*kverrors.ErrConflict); ok {
			resp.OtherError = conflictErr.Error()
		}
	}
	return resp
}

func toPBError(err error) *tipb.Error {
	if err == nil {
		return nil
	}
	perr := new(tipb.Error)
	e := errors.Cause(err)
	switch y := e.(type) {
	case *terror.Error:
		tmp := terror.ToSQLError(y)
		perr.Code = int32(tmp.Code)
		perr.Msg = tmp.Message
	case *mysql.SQLError:
		perr.Code = int32(y.Code)
		perr.Msg = y.Message
	default:
		perr.Code = int32(1)
		perr.Msg = err.Error()
	}
	return perr
}

// extractKVRanges extracts kv.KeyRanges slice from a SelectRequest.
func extractKVRanges(startKey, endKey []byte, keyRanges []*coprocessor.KeyRange, descScan bool) (kvRanges []kv.KeyRange, err error) {
	kvRanges = make([]kv.KeyRange, 0, len(keyRanges))
	for _, kran := range keyRanges {
		if bytes.Compare(kran.GetStart(), kran.GetEnd()) >= 0 {
			err = errors.Errorf("invalid range, start should be smaller than end: %v %v", kran.GetStart(), kran.GetEnd())
			return
		}

		upperKey := kran.GetEnd()
		if bytes.Compare(upperKey, startKey) <= 0 {
			continue
		}
		lowerKey := kran.GetStart()
		if len(endKey) != 0 && bytes.Compare(lowerKey, endKey) >= 0 {
			break
		}
		r := kv.KeyRange{
			StartKey: kv.Key(maxStartKey(lowerKey, startKey)),
			EndKey:   kv.Key(minEndKey(upperKey, endKey)),
		}
		kvRanges = append(kvRanges, r)
	}
	if descScan {
		reverseKVRanges(kvRanges)
	}
	return
}

func reverseKVRanges(kvRanges []kv.KeyRange) {
	for i := range len(kvRanges) / 2 {
		j := len(kvRanges) - i - 1
		kvRanges[i], kvRanges[j] = kvRanges[j], kvRanges[i]
	}
}

func maxStartKey(rangeStartKey kv.Key, regionStartKey []byte) []byte {
	if bytes.Compare([]byte(rangeStartKey), regionStartKey) > 0 {
		return []byte(rangeStartKey)
	}
	return regionStartKey
}

func minEndKey(rangeEndKey kv.Key, regionEndKey []byte) []byte {
	if len(regionEndKey) == 0 || bytes.Compare([]byte(rangeEndKey), regionEndKey) < 0 {
		return []byte(rangeEndKey)
	}
	return regionEndKey
}

const rowsPerChunk = 64

func appendRow(chunks []tipb.Chunk, data []byte, rowCnt int) []tipb.Chunk {
	if rowCnt%rowsPerChunk == 0 {
		chunks = append(chunks, tipb.Chunk{})
	}
	cur := &chunks[len(chunks)-1]
	cur.RowsData = append(cur.RowsData, data...)
	return chunks
}

// fieldTypeFromPBColumn creates a types.FieldType from tipb.ColumnInfo.
func fieldTypeFromPBColumn(col *tipb.ColumnInfo) *types.FieldType {
	charsetStr, collationStr, _ := charset.GetCharsetInfoByID(int(collate.RestoreCollationIDIfNeeded(col.GetCollation())))
	ft := &types.FieldType{}
	ft.SetType(byte(col.GetTp()))
	ft.SetFlag(uint(col.GetFlag()))
	ft.SetFlen(int(col.GetColumnLen()))
	ft.SetDecimal(int(col.GetDecimal()))
	ft.SetElems(col.Elems)
	ft.SetCharset(charsetStr)
	ft.SetCollate(collationStr)
	return ft
}

// handleCopChecksumRequest handles coprocessor check sum request.
func handleCopChecksumRequest(dbReader *dbreader.DBReader, req *coprocessor.Request) *coprocessor.Response {
	resp := &tipb.ChecksumResponse{
		Checksum:   1,
		TotalKvs:   1,
		TotalBytes: 1,
	}
	data, err := resp.Marshal()
	if err != nil {
		return &coprocessor.Response{OtherError: fmt.Sprintf("marshal checksum response error: %v", err)}
	}
	return &coprocessor.Response{Data: data}
}
