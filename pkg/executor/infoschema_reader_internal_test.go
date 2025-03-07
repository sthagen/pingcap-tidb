// Copyright 2023 PingCAP, Inc.
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

package executor

import (
	"context"
	"testing"

	"github.com/pingcap/tidb/pkg/infoschema"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/mysql"
	plannercore "github.com/pingcap/tidb/pkg/planner/core"
	"github.com/pingcap/tidb/pkg/types"
	"github.com/stretchr/testify/require"
)

func TestSetDataFromCheckConstraints(t *testing.T) {
	tblInfos := []*model.TableInfo{
		{
			ID:    1,
			Name:  ast.NewCIStr("t1"),
			State: model.StatePublic,
		},
		{
			ID:   2,
			Name: ast.NewCIStr("t2"),
			Columns: []*model.ColumnInfo{
				{
					Name:      ast.NewCIStr("id"),
					FieldType: *types.NewFieldType(mysql.TypeLonglong),
					State:     model.StatePublic,
				},
			},
			Constraints: []*model.ConstraintInfo{
				{
					Name:       ast.NewCIStr("t2_c1"),
					Table:      ast.NewCIStr("t2"),
					ExprString: "id<10",
					State:      model.StatePublic,
				},
			},
			State: model.StatePublic,
		},
		{
			ID:   3,
			Name: ast.NewCIStr("t3"),
			Columns: []*model.ColumnInfo{
				{
					Name:      ast.NewCIStr("id"),
					FieldType: *types.NewFieldType(mysql.TypeLonglong),
					State:     model.StatePublic,
				},
			},
			Constraints: []*model.ConstraintInfo{
				{
					Name:       ast.NewCIStr("t3_c1"),
					Table:      ast.NewCIStr("t3"),
					ExprString: "id<10",
					State:      model.StateDeleteOnly,
				},
			},
			State: model.StatePublic,
		},
	}
	mockIs := infoschema.MockInfoSchema(tblInfos)
	mt := memtableRetriever{is: mockIs, extractor: &plannercore.InfoSchemaCheckConstraintsExtractor{}}
	sctx := defaultCtx()
	err := mt.setDataFromCheckConstraints(context.Background(), sctx)
	require.NoError(t, err)

	require.Equal(t, 1, len(mt.rows))    // 1 row
	require.Equal(t, 4, len(mt.rows[0])) // 4 columns
	require.Equal(t, types.NewStringDatum("def"), mt.rows[0][0])
	require.Equal(t, types.NewStringDatum("test"), mt.rows[0][1])
	require.Equal(t, types.NewStringDatum("t2_c1"), mt.rows[0][2])
	require.Equal(t, types.NewStringDatum("(id<10)"), mt.rows[0][3])
}

func TestSetDataFromTiDBCheckConstraints(t *testing.T) {
	mt := memtableRetriever{}
	sctx := defaultCtx()
	tblInfos := []*model.TableInfo{
		{
			ID:    1,
			Name:  ast.NewCIStr("t1"),
			State: model.StatePublic,
		},
		{
			ID:   2,
			Name: ast.NewCIStr("t2"),
			Columns: []*model.ColumnInfo{
				{
					Name:      ast.NewCIStr("id"),
					FieldType: *types.NewFieldType(mysql.TypeLonglong),
					State:     model.StatePublic,
				},
			},
			Constraints: []*model.ConstraintInfo{
				{
					Name:       ast.NewCIStr("t2_c1"),
					Table:      ast.NewCIStr("t2"),
					ExprString: "id<10",
					State:      model.StatePublic,
				},
			},
			State: model.StatePublic,
		},
		{
			ID:   3,
			Name: ast.NewCIStr("t3"),
			Columns: []*model.ColumnInfo{
				{
					Name:      ast.NewCIStr("id"),
					FieldType: *types.NewFieldType(mysql.TypeLonglong),
					State:     model.StatePublic,
				},
			},
			Constraints: []*model.ConstraintInfo{
				{
					Name:       ast.NewCIStr("t3_c1"),
					Table:      ast.NewCIStr("t3"),
					ExprString: "id<10",
					State:      model.StateDeleteOnly,
				},
			},
			State: model.StatePublic,
		},
	}
	mockIs := infoschema.MockInfoSchema(tblInfos)
	mt.is = mockIs
	mt.extractor = &plannercore.InfoSchemaTiDBCheckConstraintsExtractor{}
	err := mt.setDataFromTiDBCheckConstraints(context.Background(), sctx)
	require.NoError(t, err)

	require.Equal(t, 1, len(mt.rows))    // 1 row
	require.Equal(t, 6, len(mt.rows[0])) // 6 columns
	require.Equal(t, types.NewStringDatum("def"), mt.rows[0][0])
	require.Equal(t, types.NewStringDatum("test"), mt.rows[0][1])
	require.Equal(t, types.NewStringDatum("t2_c1"), mt.rows[0][2])
	require.Equal(t, types.NewStringDatum("(id<10)"), mt.rows[0][3])
	require.Equal(t, types.NewStringDatum("t2"), mt.rows[0][4])
	require.Equal(t, types.NewIntDatum(2), mt.rows[0][5])
}

func TestSetDataFromKeywords(t *testing.T) {
	mt := memtableRetriever{}
	err := mt.setDataFromKeywords()
	require.NoError(t, err)
	require.Equal(t, types.NewStringDatum("ADD"), mt.rows[0][0]) // Keyword: ADD
	require.Equal(t, types.NewIntDatum(1), mt.rows[0][1])        // Reserved: true(1)
}
