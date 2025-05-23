// Copyright 2020 PingCAP, Inc. Licensed under Apache-2.0.

package util

import (
	"context"

	"github.com/pingcap/errors"
	"github.com/pingcap/kvproto/pkg/metapb"
	berrors "github.com/pingcap/tidb/br/pkg/errors"
	"github.com/pingcap/tidb/pkg/util/engine"
	"github.com/tikv/pd/client/opt"
)

// StoreBehavior is the action to do in GetAllTiKVStores when a non-TiKV
// store (e.g. TiFlash store) is found.
type StoreBehavior uint8

const (
	// ErrorOnTiFlash causes GetAllTiKVStores to return error when the store is
	// found to be a TiFlash node.
	ErrorOnTiFlash StoreBehavior = 0
	// SkipTiFlash causes GetAllTiKVStores to skip the store when it is found to
	// be a TiFlash node.
	SkipTiFlash StoreBehavior = 1
	// TiFlashOnly caused GetAllTiKVStores to skip the store which is not a
	// TiFlash node.
	TiFlashOnly StoreBehavior = 2
)

// StoreMeta is the required interface for a watcher.
// It is striped from pd.Client.
type StoreMeta interface {
	// GetAllStores gets all stores from pd.
	// The store may expire later. Caller is responsible for caching and taking care
	// of store change.
	GetAllStores(ctx context.Context, opts ...opt.GetStoreOption) ([]*metapb.Store, error)
}

// GetAllTiKVStores returns all TiKV stores registered to the PD client. The
// stores must not be a tombstone and must never contain a label `engine=tiflash`.
func GetAllTiKVStores(
	ctx context.Context,
	pdClient StoreMeta,
	storeBehavior StoreBehavior,
) ([]*metapb.Store, error) {
	// get all live stores.
	stores, err := pdClient.GetAllStores(ctx, opt.WithExcludeTombstone())
	if err != nil {
		return nil, errors.Trace(err)
	}

	// filter out all stores which are TiFlash.
	j := 0
	for _, store := range stores {
		isTiFlash := false
		if engine.IsTiFlash(store) {
			if storeBehavior == SkipTiFlash {
				continue
			} else if storeBehavior == ErrorOnTiFlash {
				return nil, errors.Annotatef(berrors.ErrPDInvalidResponse,
					"cannot restore to a cluster with active TiFlash stores (store %d at %s)", store.Id, store.Address)
			}
			isTiFlash = true
		}
		if !isTiFlash && storeBehavior == TiFlashOnly {
			continue
		}
		stores[j] = store
		j++
	}
	return stores[:j], nil
}
