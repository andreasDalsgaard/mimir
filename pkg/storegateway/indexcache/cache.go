// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/thanos-io/thanos/blob/main/pkg/store/cache/cache.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Thanos Authors.

package indexcache

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"sort"
	"strings"

	"github.com/oklog/ulid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"golang.org/x/crypto/blake2b"

	"github.com/grafana/mimir/pkg/storage/sharding"
)

const (
	cacheTypePostings          = "Postings"
	cacheTypeSeriesForRef      = "SeriesForRef"
	cacheTypeExpandedPostings  = "ExpandedPostings"
	cacheTypeSeries            = "Series"
	cacheTypeSeriesForPostings = "SeriesForPostings"
	cacheTypeLabelNames        = "LabelNames"
	cacheTypeLabelValues       = "LabelValues"
)

var (
	allCacheTypes = []string{
		cacheTypePostings,
		cacheTypeSeriesForRef,
		cacheTypeExpandedPostings,
		cacheTypeSeries,
		cacheTypeSeriesForPostings,
		cacheTypeLabelNames,
		cacheTypeLabelValues,
	}
)

// IndexCache is the interface exported by index cache backends.
type IndexCache interface {
	// StorePostings stores postings for a single series.
	StorePostings(ctx context.Context, userID string, blockID ulid.ULID, l labels.Label, v []byte)

	// FetchMultiPostings fetches multiple postings - each identified by a label -
	// and returns a map containing cache hits, along with a list of missing keys.
	FetchMultiPostings(ctx context.Context, userID string, blockID ulid.ULID, keys []labels.Label) (hits map[labels.Label][]byte, misses []labels.Label)

	// StoreSeriesForRef stores a single series.
	StoreSeriesForRef(ctx context.Context, userID string, blockID ulid.ULID, id storage.SeriesRef, v []byte)

	// FetchMultiSeriesForRefs fetches multiple series - each identified by ID - from the cache
	// and returns a map containing cache hits, along with a list of missing IDs.
	FetchMultiSeriesForRefs(ctx context.Context, userID string, blockID ulid.ULID, ids []storage.SeriesRef) (hits map[storage.SeriesRef][]byte, misses []storage.SeriesRef)

	// StoreExpandedPostings stores the result of ExpandedPostings, encoded with an unspecified codec.
	StoreExpandedPostings(ctx context.Context, userID string, blockID ulid.ULID, key LabelMatchersKey, v []byte)

	// FetchExpandedPostings fetches the result of ExpandedPostings, encoded with an unspecified codec.
	FetchExpandedPostings(ctx context.Context, userID string, blockID ulid.ULID, key LabelMatchersKey) ([]byte, bool)

	// StoreSeries stores the result of a Series() call.
	StoreSeries(ctx context.Context, userID string, blockID ulid.ULID, matchersKey LabelMatchersKey, shard *sharding.ShardSelector, v []byte)
	// FetchSeries fetches the result of a Series() call.
	FetchSeries(ctx context.Context, userID string, blockID ulid.ULID, matchersKey LabelMatchersKey, shard *sharding.ShardSelector) ([]byte, bool)

	// StoreSeriesForPostings stores a series set for the provided postings.
	StoreSeriesForPostings(ctx context.Context, userID string, blockID ulid.ULID, matchersKey LabelMatchersKey, shard *sharding.ShardSelector, postingsKey PostingsKey, v []byte)
	// FetchSeriesForPostings fetches a series set for the provided postings.
	FetchSeriesForPostings(ctx context.Context, userID string, blockID ulid.ULID, matchersKey LabelMatchersKey, shard *sharding.ShardSelector, postingsKey PostingsKey) ([]byte, bool)

	// StoreLabelNames stores the result of a LabelNames() call.
	StoreLabelNames(ctx context.Context, userID string, blockID ulid.ULID, matchersKey LabelMatchersKey, v []byte)
	// FetchLabelNames fetches the result of a LabelNames() call.
	FetchLabelNames(ctx context.Context, userID string, blockID ulid.ULID, matchersKey LabelMatchersKey) ([]byte, bool)

	// StoreLabelValues stores the result of a LabelValues() call.
	StoreLabelValues(ctx context.Context, userID string, blockID ulid.ULID, labelName string, matchersKey LabelMatchersKey, v []byte)
	// FetchLabelValues fetches the result of a LabelValues() call.
	FetchLabelValues(ctx context.Context, userID string, blockID ulid.ULID, labelName string, matchersKey LabelMatchersKey) ([]byte, bool)
}

// PostingsKey represents a canonical key for a []storage.SeriesRef slice
type PostingsKey string

// CanonicalPostingsKey creates a canonical version of PostingsKey
func CanonicalPostingsKey(postings []storage.SeriesRef) PostingsKey {
	hashable := make([]byte, len(postings)*8)
	for i, posting := range postings {
		for octet := 0; octet < 8; octet++ {
			hashable[i+octet] = byte(posting >> (octet * 8))
		}
	}

	// We hash the postings list twice to minimize the chance of collisions
	hasher1, _ := blake2b.New256(nil) // This will never return an error
	hasher2 := sha1.New()

	_, _ = hasher1.Write(hashable)
	_, _ = hasher2.Write(hashable)

	checksum := hasher2.Sum(hasher1.Sum(nil))

	return PostingsKey(base64.RawURLEncoding.EncodeToString(checksum))
}

// LabelMatchersKey represents a canonical key for a []*matchers.Matchers slice
type LabelMatchersKey string

// CanonicalLabelMatchersKey creates a canonical version of LabelMatchersKey
func CanonicalLabelMatchersKey(ms []*labels.Matcher) LabelMatchersKey {
	sorted := make([]labels.Matcher, len(ms))
	for i := range ms {
		sorted[i] = labels.Matcher{Type: ms[i].Type, Name: ms[i].Name, Value: ms[i].Value}
	}
	sort.Sort(sortedLabelMatchers(sorted))

	const (
		typeLen = 2
		sepLen  = 1
	)
	var size int
	for _, m := range sorted {
		size += len(m.Name) + len(m.Value) + typeLen + sepLen
	}
	sb := strings.Builder{}
	sb.Grow(size)
	for _, m := range sorted {
		sb.WriteString(m.Name)
		sb.WriteString(m.Type.String())
		sb.WriteString(m.Value)
		sb.WriteByte(0)
	}
	return LabelMatchersKey(sb.String())
}

type sortedLabelMatchers []labels.Matcher

func (c sortedLabelMatchers) Less(i, j int) bool {
	if c[i].Name != c[j].Name {
		return c[i].Name < c[j].Name
	}
	if c[i].Type != c[j].Type {
		return c[i].Type < c[j].Type
	}
	return c[i].Value < c[j].Value
}

func (c sortedLabelMatchers) Len() int      { return len(c) }
func (c sortedLabelMatchers) Swap(i, j int) { c[i], c[j] = c[j], c[i] }

func initLabelValuesForAllCacheTypes(vec *prometheus.MetricVec) {
	for _, typ := range allCacheTypes {
		_, err := vec.GetMetricWithLabelValues(typ)
		if err != nil {
			panic(err)
		}
	}
}

func shardKey(shard *sharding.ShardSelector) string {
	if shard == nil {
		return "all"
	}
	return shard.LabelValue()
}
