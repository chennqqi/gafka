package gateway

import (
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/funkygao/gafka/cmd/kateway/job"
	"github.com/funkygao/gafka/cmd/kateway/manager"
	"github.com/funkygao/gafka/mpool"
	"github.com/funkygao/httprouter"
	log "github.com/funkygao/log4go"
)

//go:generate goannotation $GOFILE
// @rest POST /v1/jobs/:topic/:ver?delay=100|due=1471565204
// TODO tag, partitionKey
// TODO use dedicated metrics
func (this *pubServer) addJobHandler(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	if !Options.DisableMetrics {
		this.pubMetrics.JobTryQps.Mark(1)
	}

	t1 := time.Now()
	realIp := getHttpRemoteIp(r)
	appid := r.Header.Get(HttpHeaderAppid)

	var due int64
	q := r.URL.Query()
	dueParam := q.Get("due") // due has higher priority than delay
	if dueParam != "" {
		d, err := strconv.ParseInt(dueParam, 10, 64)
		if err != nil {
			log.Error("+job[%s] %s(%s) due:%s %s", appid, r.RemoteAddr, realIp, dueParam, err)

			writeBadRequest(w, "invalid due param")
			return
		}

		due = d
	} else {
		delayParam := q.Get("delay") // in sec
		delay, err := strconv.ParseInt(delayParam, 10, 64)
		if err != nil {
			log.Error("+job[%s] %s(%s) delay:%s %s", appid, r.RemoteAddr, realIp, delayParam, err)

			writeBadRequest(w, "invalid delay param")
			return
		}

		due = t1.Unix() + delay
	}

	if due <= t1.Unix() {
		log.Error("+job[%s] %s(%s) due=%d before now?", appid, r.RemoteAddr, realIp, due)

		writeBadRequest(w, "invalid param")
		return
	}

	if Options.Ratelimit && !this.throttlePub.Pour(realIp, 1) {
		log.Warn("+job[%s] %s(%s) rate limit reached", appid, r.RemoteAddr, realIp)

		writeQuotaExceeded(w)
		return
	}

	topic := params.ByName(UrlParamTopic)
	ver := params.ByName(UrlParamVersion)
	if err := manager.Default.OwnTopic(appid, r.Header.Get(HttpHeaderPubkey), topic); err != nil {
		log.Warn("+job[%s] %s(%s) {topic:%s, ver:%s} %s", appid, r.RemoteAddr, realIp, topic, ver, err)

		writeAuthFailure(w, err)
		return
	}

	// get the raw POST message
	msgLen := int(r.ContentLength)
	switch {
	case msgLen == -1:
		log.Warn("+job[%s] %s(%s) {topic:%s, ver:%s} invalid content length: %d",
			appid, r.RemoteAddr, realIp, topic, ver, msgLen)

		writeBadRequest(w, "invalid content length")
		return

	case int64(msgLen) > Options.MaxJobSize:
		log.Warn("+job[%s] %s(%s) {topic:%s, ver:%s} too big content length: %d",
			appid, r.RemoteAddr, realIp, topic, ver, msgLen)
		writeBadRequest(w, ErrTooBigMessage.Error())
		return

	case msgLen < Options.MinPubSize:
		log.Warn("+job[%s] %s(%s) {topic:%s, ver:%s} too small content length: %d",
			appid, r.RemoteAddr, realIp, topic, ver, msgLen)
		writeBadRequest(w, ErrTooSmallMessage.Error())
		return
	}

	lbr := io.LimitReader(r.Body, Options.MaxJobSize+1)
	msg := mpool.NewMessage(msgLen)
	msg.Body = msg.Body[0:msgLen]
	if _, err := io.ReadAtLeast(lbr, msg.Body, msgLen); err != nil {
		msg.Free()

		log.Error("+job[%s] %s(%s) {topic:%s, ver:%s} %s",
			appid, r.RemoteAddr, realIp, topic, ver, err)
		writeBadRequest(w, ErrTooBigMessage.Error()) // TODO http.StatusRequestEntityTooLarge
		return
	}

	log.Debug("+job[%s] %s(%s) {topic:%s, ver:%s} due:%d/%ds",
		appid, r.RemoteAddr, realIp, topic, ver, due, due-t1.Unix())

	if !Options.DisableMetrics {
		this.pubMetrics.JobQps.Mark(1)
		this.pubMetrics.JobMsgSize.Update(int64(len(msg.Body)))
	}

	_, found := manager.Default.LookupCluster(appid)
	if !found {
		msg.Free()

		log.Error("+job[%s] %s(%s) {topic:%s, ver:%s} cluster not found",
			appid, r.RemoteAddr, realIp, topic, ver)

		writeBadRequest(w, "invalid appid")
		return
	}

	jobId, err := job.Default.Add(appid, manager.Default.KafkaTopic(appid, topic, ver), msg.Body, due)
	msg.Free()
	if err != nil {
		if !Options.DisableMetrics {
			this.pubMetrics.PubFail(appid, topic, ver)
		}

		log.Error("+job[%s] %s(%s) {topic:%s, ver:%s} %s",
			appid, r.RemoteAddr, realIp, topic, ver, err)
		writeServerError(w, err.Error())
		return
	}

	if Options.AuditPub {
		this.auditor.Trace("+job[%s] %s(%s) {topic:%s ver:%s UA:%s} due:%d id:%s",
			appid, r.RemoteAddr, realIp, topic, ver, r.Header.Get("User-Agent"), due, jobId)
	}

	w.Header().Set(HttpHeaderJobId, jobId)
	w.WriteHeader(http.StatusCreated)

	if _, err = w.Write(ResponseOk); err != nil {
		log.Error("%s: %v", r.RemoteAddr, err)
		this.pubMetrics.ClientError.Inc(1)
	}

	if !Options.DisableMetrics {
		this.pubMetrics.PubOk(appid, topic, ver)
		this.pubMetrics.PubLatency.Update(time.Since(t1).Nanoseconds() / 1e6) // in ms
	}
}

// DELETE /v1/jobs/:topic/:ver?id=22323
func (this *pubServer) deleteJobHandler(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	appid := r.Header.Get(HttpHeaderAppid)
	topic := params.ByName(UrlParamTopic)
	ver := params.ByName(UrlParamVersion)
	realIp := getHttpRemoteIp(r)
	if err := manager.Default.OwnTopic(appid, r.Header.Get(HttpHeaderPubkey), topic); err != nil {
		log.Error("-job[%s] %s(%s) {topic:%s, ver:%s} %s",
			appid, r.RemoteAddr, realIp, topic, ver, err)

		writeAuthFailure(w, err)
		return
	}

	_, found := manager.Default.LookupCluster(appid)
	if !found {
		log.Error("-job[%s] %s(%s) {topic:%s, ver:%s} cluster not found",
			appid, r.RemoteAddr, realIp, topic, ver)

		writeBadRequest(w, "invalid appid")
		return
	}

	jobId := r.URL.Query().Get("id")
	if len(jobId) < 18 { // jobId e,g. 341647700585877504
		writeBadRequest(w, "invalid job id")
		return
	}

	if err := job.Default.Delete(appid, manager.Default.KafkaTopic(appid, topic, ver), jobId); err != nil {
		if err == job.ErrNothingDeleted {
			// race failed, actor worker wins
			log.Warn("-job[%s] %s(%s) {topic:%s, ver:%s jid:%s} %v",
				appid, r.RemoteAddr, realIp, topic, ver, jobId, err)

			w.WriteHeader(http.StatusConflict)
			w.Write([]byte{})
			return
		}

		log.Error("-job[%s] %s(%s) {topic:%s, ver:%s jid:%s} %v",
			appid, r.RemoteAddr, realIp, topic, ver, jobId, err)

		writeServerError(w, err.Error())
		return
	}

	if Options.AuditPub {
		this.auditor.Trace("-job[%s] %s(%s) {topic:%s ver:%s UA:%s jid:%s}",
			appid, r.RemoteAddr, realIp, topic, ver, r.Header.Get("User-Agent"), jobId)
	}

	w.Write(ResponseOk)
}
