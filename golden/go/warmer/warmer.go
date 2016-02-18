// warmer makes sure we've pre-warmed the cache for normal queries.
//
// This is a quick fix until filediffstore does full NxM diffs for every
// untriaged digest by default.
package warmer

import (
	"sync"
	"time"

	"github.com/skia-dev/glog"
	"go.skia.org/infra/go/timer"
	"go.skia.org/infra/go/util"
	"go.skia.org/infra/golden/go/digesttools"
	"go.skia.org/infra/golden/go/storage"
	"go.skia.org/infra/golden/go/summary"
	"go.skia.org/infra/golden/go/tally"
	"go.skia.org/infra/golden/go/types"
)

func Init(storages *storage.Storage, summaries *summary.Summaries, tallies *tally.Tallies) error {
	exp, err := storages.ExpectationsStore.Get()
	if err != nil {
		return err
	}
	go func() {
		oneRun := func() {
			t := timer.New("warmer one loop")
			for test, sum := range summaries.Get() {
				for _, digest := range sum.UntHashes {
					t := tallies.ByTest()[test]
					if t != nil {
						// Calculate the closest digest for the side effect of filling in the filediffstore cache.
						digesttools.ClosestDigest(test, digest, exp, t, storages.DiffStore, types.POSITIVE)
						digesttools.ClosestDigest(test, digest, exp, t, storages.DiffStore, types.NEGATIVE)
					}
				}
			}
			t.Stop()
			if newExp, err := storages.ExpectationsStore.Get(); err != nil {
				glog.Errorf("warmer: Failed to get expectations: %s", err)
			} else {
				exp = newExp
			}

			// Make sure all images are downloaded. This is necessary, because
			// the front-end doesn't get URLs (generated via DiffStore.AbsPath)
			// which ensures that the image has been downloaded.
			// TODO(stephana): Remove this once the new diffstore is in place.
			tile, err := storages.GetLastTileTrimmed(true)
			if err != nil {
				glog.Errorf("Error retrieving tile: %s", err)
			}
			tileLen := tile.LastCommitIndex() + 1
			traceDigests := make(util.StringSet, tileLen)
			for _, trace := range tile.Traces {
				gTrace := trace.(*types.GoldenTrace)
				for _, digest := range gTrace.Values {
					if digest != types.MISSING_DIGEST {
						traceDigests[digest] = true
					}
				}
			}

			digests := traceDigests.Keys()
			glog.Infof("FOUND %d digests to fetch.", len(digests))
			storages.DiffStore.AbsPath(digests)

			if err := warmTrybotDigests(storages, traceDigests); err != nil {
				glog.Errorf("Error retrieving trybot digests: %s", err)
				return
			}
		}

		oneRun()
		for _ = range time.Tick(time.Minute) {
			oneRun()
		}
	}()
	return nil
}

func warmTrybotDigests(storages *storage.Storage, traceDigests map[string]bool) error {
	issues, _, err := storages.TrybotResults.ListTrybotIssues(0, 100)
	if err != nil {
		return err
	}

	trybotDigests := util.NewStringSet()
	var wg sync.WaitGroup
	var mutex sync.Mutex
	for _, oneIssue := range issues {
		wg.Add(1)
		go func(issueID string) {
			_, tile, err := storages.TrybotResults.GetIssue(issueID, nil, true)
			if err != nil {
				glog.Errorf("Unable to retrieve issue %s. Got error: %s", issueID, err)
				return
			}

			for _, trace := range tile.Traces {
				gTrace := trace.(*types.GoldenTrace)
				for _, digest := range gTrace.Values {
					if !traceDigests[digest] {
						mutex.Lock()
						trybotDigests[digest] = true
						mutex.Unlock()
					}
				}
			}
			wg.Done()
		}(oneIssue.ID)
	}

	wg.Wait()
	digests := trybotDigests.Keys()
	glog.Infof("FOUND %d trybot digests to fetch.", len(digests))
	storages.DiffStore.AbsPath(digests)
	return nil
}
