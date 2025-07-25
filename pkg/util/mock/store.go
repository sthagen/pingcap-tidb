// Copyright 2018 PingCAP, Inc.
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

package mock

import (
	"context"

	deadlockpb "github.com/pingcap/kvproto/pkg/deadlock"
	"github.com/pingcap/tidb/pkg/kv"
	"github.com/tikv/client-go/v2/oracle"
	"github.com/tikv/client-go/v2/tikv"
)

// Store implements kv.Storage interface.
type Store struct {
	Client kv.Client
}

// GetClient implements kv.Storage interface.
func (s *Store) GetClient() kv.Client { return s.Client }

// GetMPPClient implements kv.Storage interface.
func (*Store) GetMPPClient() kv.MPPClient { return nil }

// GetOracle implements kv.Storage interface.
func (*Store) GetOracle() oracle.Oracle { return nil }

// Begin implements kv.Storage interface.
func (*Store) Begin(_ ...tikv.TxnOption) (kv.Transaction, error) { return nil, nil }

// GetSnapshot implements kv.Storage interface.
func (*Store) GetSnapshot(_ kv.Version) kv.Snapshot { return nil }

// Close implements kv.Storage interface.
func (*Store) Close() error { return nil }

// UUID implements kv.Storage interface.
func (*Store) UUID() string { return "mock" }

// CurrentVersion implements kv.Storage interface.
func (*Store) CurrentVersion(_ string) (kv.Version, error) { return kv.Version{}, nil }

// SupportDeleteRange implements kv.Storage interface.
func (*Store) SupportDeleteRange() bool { return false }

// Name implements kv.Storage interface.
func (*Store) Name() string { return "UtilMockStorage" }

// Describe implements kv.Storage interface.
func (*Store) Describe() string {
	return "UtilMockStorage is a mock Store implementation, only for unittests in util package"
}

// GetMemCache implements kv.Storage interface
func (*Store) GetMemCache() kv.MemManager {
	return nil
}

// ShowStatus implements kv.Storage interface.
func (*Store) ShowStatus(_ context.Context, _ string) (any, error) { return nil, nil }

// GetMinSafeTS implements kv.Storage interface.
func (*Store) GetMinSafeTS(_ string) uint64 {
	return 0
}

// GetLockWaits implements kv.Storage interface.
func (*Store) GetLockWaits() ([]*deadlockpb.WaitForEntry, error) {
	return nil, nil
}

// GetCodec implements kv.Storage interface.
func (*Store) GetCodec() tikv.Codec {
	return nil
}

// GetOption implements kv.Storage interface.
func (*Store) GetOption(_ any) (any, bool) {
	return nil, false
}

// SetOption implements kv.Storage interface.
func (*Store) SetOption(_, _ any) {}

// GetClusterID implements kv.Storage interface.
func (*Store) GetClusterID() uint64 {
	return 1
}

// GetKeyspace implements kv.Storage interface.
func (*Store) GetKeyspace() string {
	return ""
}
