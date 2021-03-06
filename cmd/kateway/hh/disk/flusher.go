package disk

import (
	"sync"
	"time"

	"github.com/funkygao/gafka/cmd/kateway/store"
	log "github.com/funkygao/log4go"
)

func (q *queue) FlushInflights(errCh chan<- error, wg *sync.WaitGroup) {
	defer func() {
		q.cursor.dump()
		wg.Done()
	}()

	var (
		b         block
		err       error
		partition int32
		offset    int64
		okN       int64
		backoff   = initialBackoff
	)
	for {
		backoff = initialBackoff
		err = q.Next(&b)
		switch err {
		case nil:
			for retries := 0; retries < flusherMaxRetries; retries++ {
				partition, offset, err = store.DefaultPubStore.SyncPub(q.clusterTopic.cluster, q.clusterTopic.topic, b.key, b.value)
				if err == nil {
					if Auditor != nil {
						Auditor.Trace("queue[%s] {P:%d O:%d}", q.ident(), partition, offset)
					}

					q.cursor.commitPosition()
					okN++
					q.inflights.Add(-1)
					if okN%dumpPerBlocks == 0 {
						if e := q.cursor.dump(); e != nil {
							log.Error("queue[%s] dump: %s", q.ident(), e)
						}
					}
					break
				} else if err == store.ErrInvalidTopic || err == store.ErrInvalidCluster {
					q.cursor.commitPosition()
					q.inflights.Add(-1)
					log.Warn("queue[%s] {k:%s v:%s}: %s", q.ident(), string(b.key), string(b.value), err)

					err = nil // move ahead without retry
					break
				} else {
					log.Debug("queue[%s] {k:%s v:%s}: %s", q.ident(), string(b.key), string(b.value), err)

					time.Sleep(backoff)
					backoff *= 2
					if backoff >= maxBackoff {
						backoff = maxBackoff
					}
				}
			}

			if err == nil {
				continue
			}

			errCh <- err

			if err = q.Rollback(&b); err != nil {
				// should never happen
				log.Error("queue[%s] {k:%s v:%s}: %s", q.ident(), string(b.key), string(b.value), err)
				errCh <- err
			}
			return

		case ErrQueueNotOpen:
			errCh <- err
			return

		case ErrEOQ:
			log.Debug("queue[%s] flushed %d inflights", q.ident(), okN)
			return

		default:
			errCh <- err
		}
	}
}
