// Copyright 2015 PingCAP, Inc.
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

package printer

import (
	"testing"

	"github.com/pingcap/tidb/pkg/config/kerneltype"
	"github.com/stretchr/testify/require"
)

func TestPrintResult(t *testing.T) {
	cols := []string{"col1", "col2", "col3"}
	datas := [][]string{{"11"}, {"21", "22", "23"}}
	result, ok := GetPrintResult(cols, datas)
	require.False(t, ok)
	require.Equal(t, "", result)

	datas = [][]string{{"11", "12", "13"}, {"21", "22", "23"}}
	expect := `
+------+------+------+
| col1 | col2 | col3 |
+------+------+------+
| 11   | 12   | 13   |
| 21   | 22   | 23   |
+------+------+------+
`
	result, ok = GetPrintResult(cols, datas)
	require.True(t, ok)
	require.Equal(t, expect[1:], result)

	datas = nil
	result, ok = GetPrintResult(cols, datas)
	require.False(t, ok)
	require.Equal(t, "", result)

	cols = nil
	result, ok = GetPrintResult(cols, datas)
	require.False(t, ok)
	require.Equal(t, "", result)
}

func TestGetTiDBInfo(t *testing.T) {
	info := GetTiDBInfo()
	if kerneltype.IsNextGen() {
		require.Contains(t, info, "\nKernel Type: Next Generation")
	} else {
		require.Contains(t, info, "\nKernel Type: Classic")
	}
}
