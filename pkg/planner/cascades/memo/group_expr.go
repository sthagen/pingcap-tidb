// Copyright 2024 PingCAP, Inc.
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

package memo

import (
	"unsafe"

	"github.com/bits-and-blooms/bitset"
	"github.com/pingcap/failpoint"
	"github.com/pingcap/tidb/pkg/expression"
	base2 "github.com/pingcap/tidb/pkg/planner/cascades/base"
	"github.com/pingcap/tidb/pkg/planner/cascades/pattern"
	"github.com/pingcap/tidb/pkg/planner/cascades/util"
	"github.com/pingcap/tidb/pkg/planner/core/base"
	"github.com/pingcap/tidb/pkg/planner/core/operator/logicalop"
	"github.com/pingcap/tidb/pkg/planner/property"
	"github.com/pingcap/tidb/pkg/util/intest"
)

// GroupExpression is a single expression from the equivalent list classes inside a group.
// it is a node in the expression tree, while it takes groups as inputs. This kind of loose
// coupling between Group and GroupExpression is the key to the success of the memory compact
// of representing a forest.
type GroupExpression struct {
	// LogicalPlan is internal logical expression stands for this groupExpr.
	// Define it in the header element can make GE as Logical Plan implementor.
	base.LogicalPlan

	// group is the Group that this GroupExpression belongs to.
	group *Group

	// inputs stores the Groups that this GroupExpression based on.
	Inputs []*Group

	// hash64 is the unique fingerprint of the GroupExpression.
	hash64 uint64

	// mask indicate what rules have been applied in this group expression.
	mask *bitset.BitSet

	// abandoned is used in a case, when this gE has been encapsulated (say) 3 tasks
	// and pushed into the task, this 3 task are all referring to this same gE, one
	// of them has been substituted halfway, the successive task waiting on the task
	// should feel this gE is out of date, and this task is abandoned.
	abandoned bool
}

// GetGroup returns the Group that this GroupExpression belongs to.
func (e *GroupExpression) GetGroup() *Group {
	return e.group
}

// String implements the fmt.Stringer interface.
func (e *GroupExpression) String(w util.StrBufferWriter) {
	e.LogicalPlan.ExplainID()
	w.WriteString("GE:" + e.LogicalPlan.ExplainID().String() + "{")
	for i, input := range e.Inputs {
		if i != 0 {
			w.WriteString(", ")
		}
		input.String(w)
	}
	w.WriteString("}")
}

// GetHash64 returns the cached hash64 of the GroupExpression.
func (e *GroupExpression) GetHash64() uint64 {
	intest.Assert(e.hash64 != 0, "hash64 should not be 0")
	return e.hash64
}

// Hash64 implements the Hash64 interface.
func (e *GroupExpression) Hash64(h base2.Hasher) {
	// logical plan hash.
	e.LogicalPlan.Hash64(h)
	// children group hash.
	for _, child := range e.Inputs {
		child.Hash64(h)
	}
}

// Equals implements the Equals interface.
func (e *GroupExpression) Equals(other any) bool {
	e2, ok := other.(*GroupExpression)
	if !ok {
		return false
	}
	if e == nil {
		return e2 == nil
	}
	if e2 == nil {
		return false
	}
	if len(e.Inputs) != len(e2.Inputs) {
		return false
	}
	if pattern.GetOperand(e.LogicalPlan) != pattern.GetOperand(e2.LogicalPlan) {
		return false
	}
	// current logical operator meta cmp, logical plan don't care logicalPlan's children.
	// when we convert logicalPlan to GroupExpression, we will set children to nil.
	if !e.LogicalPlan.Equals(e2.LogicalPlan) {
		return false
	}
	// if one of the children is different, then the two GroupExpressions are different.
	for i, one := range e.Inputs {
		if !one.Equals(e2.Inputs[i]) {
			return false
		}
	}
	return true
}

// Init initializes the GroupExpression with the given group and hasher.
func (e *GroupExpression) Init(h base2.Hasher) {
	e.Hash64(h)
	e.hash64 = h.Sum64()
}

// IsExplored return whether this gE has explored rule i.
func (e *GroupExpression) IsExplored(i uint) bool {
	return e.mask.Test(i)
}

// SetExplored set this gE as explored in rule i.
func (e *GroupExpression) SetExplored(i uint) {
	e.mask.Set(i)
}

// IsAbandoned returns whether this gE is abandoned.
func (e *GroupExpression) IsAbandoned() bool {
	return e.abandoned
}

// SetAbandoned set this gE as abandoned.
func (e *GroupExpression) SetAbandoned() {
	e.abandoned = true
}

// mergeTo will migrate the src GE state to dst GE and remove src GE from its group.
func (e *GroupExpression) mergeTo(target *GroupExpression) {
	e.GetGroup().Delete(e)
	// rule mask | OR
	target.mask.InPlaceUnion(e.mask)
	// clear parentGE refs work
	for _, childG := range e.Inputs {
		childG.removeParentGEs(e)
	}
	e.Inputs = e.Inputs[:0]
	e.group = nil
}

func (e *GroupExpression) addr() unsafe.Pointer {
	return unsafe.Pointer(e)
}

// GetWrappedLogicalPlan overrides the logical plan interface implemented by BaseLogicalPlan.
func (e *GroupExpression) GetWrappedLogicalPlan() base.LogicalPlan {
	return e.LogicalPlan
}

// DeriveLogicalProp derive the new group's logical property from a specific GE.
// DeriveLogicalProp is not called with recursive, because we only examine and
// init new group from bottom-up, so we can sure that this new group's children
// has already gotten its logical prop.
func (e *GroupExpression) DeriveLogicalProp() (err error) {
	if e.GetGroup().HasLogicalProperty() {
		return nil
	}
	childStats := make([]*property.StatsInfo, 0, len(e.Inputs))
	childSchema := make([]*expression.Schema, 0, len(e.Inputs))
	for _, childG := range e.Inputs {
		childGProp := childG.GetLogicalProperty()
		childStats = append(childStats, childGProp.Stats)
		childSchema = append(childSchema, childGProp.Schema)
	}
	e.GetGroup().SetLogicalProperty(property.NewLogicalProp())
	// currently the schemaProducer side logical op is still useful for group schema.
	tmpFD := e.LogicalPlan.GetBaseLogicalPlan().(*logicalop.BaseLogicalPlan).FDs()
	tmpSchema := e.LogicalPlan.Schema()
	tmpStats := e.LogicalPlan.StatsInfo()
	// the leaves node may have already had their stats in join reorder est phase, while
	// their group ndv signal is passed in CollectPredicateColumnsPoint which is applied
	// behind join reorder rule, we should build their group ndv again (implied in DeriveStats).
	skipDeriveStats := false
	failpoint.Inject("MockPlanSkipMemoDeriveStats", func(val failpoint.Value) {
		skipDeriveStats = val.(bool)
	})
	if !skipDeriveStats {
		// here can only derive the basic stats from bottom up, we can't pass any colGroups required by parents.
		tmpStats, _, err = e.LogicalPlan.DeriveStats(childStats, tmpSchema, childSchema, nil)
		if err != nil {
			return err
		}
		tmpFD = e.LogicalPlan.ExtractFD()
	}
	e.GetGroup().GetLogicalProperty().Schema = tmpSchema
	e.GetGroup().GetLogicalProperty().Stats = tmpStats
	e.GetGroup().GetLogicalProperty().FD = tmpFD
	return nil
}
