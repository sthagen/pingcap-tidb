// Copyright 2022 PingCAP, Inc.
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

package flatplan

import (
	"context"
	"fmt"
	"testing"

	"github.com/pingcap/tidb/pkg/infoschema"
	"github.com/pingcap/tidb/pkg/kv"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/planner"
	"github.com/pingcap/tidb/pkg/planner/core"
	"github.com/pingcap/tidb/pkg/planner/core/resolve"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testdata"
	"github.com/stretchr/testify/require"
)

// FlatPhysicalOperatorForTest contains fields of FlatOperator that is needed for tests.
type FlatPhysicalOperatorForTest struct {
	Depth          uint32
	Label          core.OperatorLabel
	IsRoot         bool
	StoreType      kv.StoreType
	ReqType        core.ReadReqType
	IsPhysicalPlan bool
	TextTreeIndent string
	IsLastChild    bool
}

func simplifyFlatPhysicalOperator(e *core.FlatOperator) *FlatPhysicalOperatorForTest {
	return &FlatPhysicalOperatorForTest{
		Depth:          e.Depth,
		Label:          e.Label,
		IsRoot:         e.IsRoot,
		StoreType:      e.StoreType,
		ReqType:        e.ReqType,
		TextTreeIndent: e.TextTreeIndent,
		IsLastChild:    e.IsLastChild,
		IsPhysicalPlan: e.IsPhysicalPlan,
	}
}

func simplifyFlatPlan(p []*core.FlatOperator) []*FlatPhysicalOperatorForTest {
	res := make([]*FlatPhysicalOperatorForTest, 0, len(p))
	for _, op := range p {
		res = append(res, simplifyFlatPhysicalOperator(op))
	}
	return res
}

func TestFlatPhysicalPlan(t *testing.T) {
	testkit.RunTestUnderCascades(t, func(t *testing.T, testKit *testkit.TestKit, cascades, caller string) {
		testKit.MustExec("use test")

		var input []string
		var output []struct {
			SQL  string
			Main []*FlatPhysicalOperatorForTest
			CTEs [][]*FlatPhysicalOperatorForTest
		}
		planSuiteData := GetFlatPlanSuiteData()
		planSuiteData.LoadTestCases(t, &input, &output, cascades, caller)
		p := parser.New()
		is := infoschema.MockInfoSchema([]*model.TableInfo{core.MockSignedTable(), core.MockUnsignedTable()})

		for i, test := range input {
			comment := fmt.Sprintf("case:%v sql:%s", i, test)
			stmt, err := p.ParseOneStmt(test, "", "")
			require.NoError(t, err, comment)
			nodeW := resolve.NewNodeW(stmt)
			p, _, err := planner.Optimize(context.Background(), testKit.Session(), nodeW, is)
			require.NoError(t, err, comment)

			explained := core.FlattenPhysicalPlan(p, false)
			main := simplifyFlatPlan(explained.Main)
			var ctes [][]*FlatPhysicalOperatorForTest
			for _, cte := range explained.CTEs {
				ctes = append(ctes, simplifyFlatPlan(cte))
			}

			testdata.OnRecord(func() {
				output[i].SQL = test
				output[i].Main = main
				output[i].CTEs = ctes
			})
			require.Equal(t, output[i].Main, main)
			require.Equal(t, output[i].CTEs, ctes)
		}
	})
}
