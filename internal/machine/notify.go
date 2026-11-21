package machine

import (
	"bytes"
	"context"
	"maps"
	"os"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/samir2015dzh/aide/gen/go/aide/identity/v1"
	relayv1 "github.com/samir2015dzh/aide/gen/go/aide/relay/v1"
	sessionv1 "github.com/samir2015dzh/aide/gen/go/aide/session/v1"
	"github.com/samir2015dzh/aide/internal/provider"
	"github.com/samir2015dzh/aide/pkg/identity"
	"github.com/samir2015dzh/aide/pkg/transport"
)

// Notices for the owner's iPhones. When a session asks something (held
// back while the owner's desktop has it: holdQuestion), fails or
// finishes a turn, the machine seals a notice to the notice key of each
// trusted device that asked for that kind (its push registration, signed by
// its device key, which a root key certifies) and hands the ciphertext to
// the relay for the push service. The relay never sees what it says. A
// new session is announced too (only agents start sessions), and so is
// what Her has for the owner while she runs here.

// pushTarget is a verified push registration.
type pushTarget struct {
	keyID string
	key   []byte
	kinds map[string]bool
}

// noticeSession is what a notice says about its session, copied while the
// session is locked.
type noticeSession struct {
	id, title, createdBy, project, agent string
	// asker asked for the turn the notice is about; askerConv and
	// creatorConv are where news goes for the asker and the creator.
	asker, askerConv, creatorConv string
	// failure is why the session crashed, for the status that says so.
	failure *sessionv1.Failure
}

// notices keeps the push targets and the notices the relay has not taken.
type notices struct {
	mu      sync.Mutex
	targets []pushTarget
	// waiting is, per thread, the newest notice the relay has not taken:
	// the machine was not connected, or the call failed. It goes out once
	// the machine is connected again (sendWaitingNotices).
	waiting map[string]waitingNotice
	// dropped is, per thread, the kind of notice dropped last and when
	// (dropWaitingNotice): one of that kind from before then that comes back
	// unsent, its call failing after the answer, is not kept (keepNotice).
	dropped map[string]droppedNotice
}

// droppedNotice is when a thread's notice of kind stopped being news.
type droppedNotice struct {
	at   time.Time
	kind string
}

// waitingNotice is a notice sealed for each device, kept for the relay.
type waitingNotice struct {
	at     time.Time
	kind   string
	sealed []*relayv1.SealedNotice
}

const (
	// noticeKept: a notice older than this is not sent any more (the push
	// service keeps one an hour too).
	noticeKept = time.Hour
	// noticesKept: at most this many threads wait; the oldest goes first.
	noticesKept = 64
)

func (n *Machine) acceptPushTargets(pts *relayv1.PushTargets) {
	n.mu.Lock()
	trust, owner := n.trust, n.ownerID
	n.mu.Unlock()
	if trust == nil {
		return
	}
	var ts []pushTarget
	for _, pt := range pts.GetTargets() {
		dc, _, err := trust.VerifyDeviceCert(pt.DeviceCert)
		if err != nil {
			continue
		}
		reg := &identityv1.PushRegistration{}
		pub, err := identity.SelfSigned(pt.Registration, reg, func(r *identityv1.PushRegistration) []byte { return r.DevicePublicKey })
		if err != nil || reg.OwnerId != owner || !bytes.Equal(reg.DevicePublicKey, dc.DevicePublicKey) || len(reg.NoticePublicKey) != 32 {
			continue
		}
		kinds := map[string]bool{}
		for _, k := range reg.Kinds {
			kinds[k] = true
		}
		ts = append(ts, pushTarget{keyID: identity.KeyID(pub), key: reg.NoticePublicKey, kinds: kinds})
	}
	n.notices.mu.Lock()
	n.notices.targets = ts
	n.notices.mu.Unlock()
}

// sessionNotice tells the owner's devices of event e, if it is one they
// asked for.
func (n *Machine) sessionNotice(s noticeSession, e *sessionv1.SessionEvent) {
	nt := &sessionv1.Notice{At: timestamppb.Now(), SessionId: s.id, Title: s.title, Project: s.project, Agent: s.agent}
	switch k := e.Kind.(type) {
	case *sessionv1.SessionEvent_QuestionAsked:
		nt.Kind = "question"
		nt.Question = trimQuestion(k.QuestionAsked.Question)
		n.holdQuestion(nt, s, k.QuestionAsked.Question.GetQuestionId())
		return
	case *sessionv1.SessionEvent_TurnCompleted:
		tc := k.TurnCompleted
		nt.Turn = tc.Turn
		if f := tc.Failure; f != nil {
			if f.Kind == sessionv1.FailureKind_FAILURE_KIND_INTERRUPTED {
				return
			}
			nt.Kind = "failure"
			nt.Failure = f.Kind
			nt.FailureMessage, _ = provider.Truncate(f.Message, 300)
		} else {
			nt.Kind = "turn_done"
			nt.DiffStat = tc.DiffStat
		}
	case *sessionv1.SessionEvent_Status:
		switch k.Status.Status {
		case sessionv1.SessionStatus_SESSION_STATUS_QUEUED: // only from Start
			nt.Kind = "session_started"
		case sessionv1.SessionStatus_SESSION_STATUS_CRASHED:
			nt.Kind = "failure"
			nt.Failure = s.failure.GetKind()
			nt.FailureMessage, _ = provider.Truncate(s.failure.GetMessage(), 300)
		default:
			return
		}
	case *sessionv1.SessionEvent_QuestionResolved:
		n.dropWaitingNotice(s.id, "question") // answered meanwhile: nothing to ask
		return
	default:
		return
	}
	n.notify(nt, s.id, n.fillSessionNotice(nt, s))
}

func (n *Machine) fillSessionNotice(nt *sessionv1.Notice, s noticeSession) func() {
	return func() {
		nt.Grant = n.knownClients()[s.createdBy].Label
		nt.Machine = n.Name()
	}
}

// The delays of a question's notice to phones (docs/session-states.md §7),
// AIDE_QUESTION_DESKTOP_GRACE and AIDE_QUESTION_ESCALATE_AFTER (a Go
// duration) for tests.
const (
	// questionDesktopGrace: how long a desktop has to say it told the owner
	// of a question (MarkSessionSeen) before phones are told at once, as
	// when the owner has no desktop running.
	questionDesktopGrace = 10 * time.Second
	// questionEscalateAfter: a question a desktop told of, the owner not
	// there, still unanswered this long after it was asked goes to phones.
	questionEscalateAfter = 3 * time.Minute
)

func questionDelay(env string, d time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(env)); err == nil && v >= 0 {
		return v
	}
	return d
}

// holdQuestion tells phones of a question only if the owner's desktop did
// not take it: at once (after the desktop's grace) when no desktop told of
// it, after questionEscalateAfter when one did but the owner was not at it
// and it is still unanswered, and never once it is answered or the owner
// was at a desktop (or opened the session) while it waited.
func (n *Machine) holdQuestion(nt *sessionv1.Notice, s noticeSession, qid string) {
	asked := time.Now()
	after := questionDelay("AIDE_QUESTION_ESCALATE_AFTER", questionEscalateAfter)
	var check func()
	check = func() {
		pending, seen := n.sessions.questionSeen(s.id)
		if pending != qid || seen.GetQuestionId() == qid && seen.GetQuestionPresent() {
			return // answered, or the owner was there
		}
		if seen.GetQuestionId() == qid { // a desktop told the owner, who was away
			if wait := time.Until(asked.Add(after)); wait > 0 {
				time.AfterFunc(wait, check)
				return
			}
		}
		n.notify(nt, s.id, n.fillSessionNotice(nt, s))
	}
	time.AfterFunc(questionDelay("AIDE_QUESTION_DESKTOP_GRACE", questionDesktopGrace), check)
}

// notify seals nt to the notice key of each device that asked for its
// kind, completed by fill once one did, and hands the ciphertexts to the
// relay.
func (n *Machine) notify(nt *sessionv1.Notice, thread string, fill func()) {
	n.notices.mu.Lock()
	targets := n.notices.targets
	n.notices.mu.Unlock()
	var out []*relayv1.SealedNotice
	for _, t := range targets {
		if !t.kinds[nt.Kind] {
			continue
		}
		if len(out) == 0 {
			fill()
		}
		sealed, err := identity.SealNotice(t.key, nt)
		if err != nil {
			continue
		}
		out = append(out, &relayv1.SealedNotice{DeviceKeyId: t.keyID, Sealed: sealed, Kind: nt.Kind, Thread: thread})
	}
	if len(out) == 0 {
		return
	}
	w := waitingNotice{at: time.Now(), kind: nt.Kind, sealed: out}
	n.notices.mu.Lock()
	delete(n.notices.waiting, thread) // this one is newer
	n.notices.mu.Unlock()
	n.mu.Lock()
	conn := n.conn
	n.mu.Unlock()
	if conn == nil {
		n.keepNotice(thread, w)
		return
	}
	go func() {
		if err := n.handNotice(conn, w); err != nil {
			n.log.Debug("notice kept for later", "kind", w.kind, "thread", thread, "err", err)
			n.keepNotice(thread, w)
		}
	}()
}

// handNotice hands a sealed notice to the relay.
func (n *Machine) handNotice(conn *transport.RelayConn, w waitingNotice) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := n.control(conn)
	if err != nil {
		return err
	}
	_, err = c.Notify(ctx, &relayv1.NotifyRequest{Notices: w.sealed})
	return err
}

// keepNotice keeps w for when the machine is connected again, unless a
// newer one of its thread waits already.
func (n *Machine) keepNotice(thread string, w waitingNotice) {
	n.notices.mu.Lock()
	defer n.notices.mu.Unlock()
	if cur, ok := n.notices.waiting[thread]; ok && cur.at.After(w.at) {
		return
	}
	// Dropped while it was on its way: a question answered while the call
	// that failed was still trying (the machine not yet aware the relay
	// had gone).
	if d, ok := n.notices.dropped[thread]; ok && d.kind == w.kind && !d.at.Before(w.at) {
		return
	}
	if n.notices.waiting == nil {
		n.notices.waiting = map[string]waitingNotice{}
	}
	n.notices.waiting[thread] = w
	for len(n.notices.waiting) > noticesKept {
		oldest := ""
		for t, x := range n.notices.waiting {
			if oldest == "" || x.at.Before(n.notices.waiting[oldest].at) {
				oldest = t
			}
		}
		delete(n.notices.waiting, oldest)
	}
}

// dropWaitingNotice forgets the thread's waiting notice if it is of kind,
// and one of kind still on its way to the relay if its call fails.
func (n *Machine) dropWaitingNotice(thread, kind string) {
	n.notices.mu.Lock()
	defer n.notices.mu.Unlock()
	if w, ok := n.notices.waiting[thread]; ok && w.kind == kind {
		delete(n.notices.waiting, thread)
	}
	now := time.Now()
	if n.notices.dropped == nil {
		n.notices.dropped = map[string]droppedNotice{}
	}
	// A call gives up within a minute (handNotice's timeout): older marks
	// can't matter any more.
	for t, d := range n.notices.dropped {
		if now.Sub(d.at) > time.Minute {
			delete(n.notices.dropped, t)
		}
	}
	n.notices.dropped[thread] = droppedNotice{at: now, kind: kind}
}

// sendWaitingNotices hands the relay the notices that waited for a
// connection, oldest first; those it does not take wait on.
func (n *Machine) sendWaitingNotices(conn *transport.RelayConn) {
	n.notices.mu.Lock()
	waiting := n.notices.waiting
	n.notices.waiting = nil
	n.notices.mu.Unlock()
	threads := slices.SortedFunc(maps.Keys(waiting), func(a, b string) int { return waiting[a].at.Compare(waiting[b].at) })
	for i, t := range threads {
		w := waiting[t]
		if time.Since(w.at) > noticeKept {
			continue
		}
		if err := n.handNotice(conn, w); err != nil {
			n.log.Debug("notices kept for later", "count", len(threads)-i, "err", err)
			for _, t := range threads[i:] {
				n.keepNotice(t, waiting[t])
			}
			return
		}
	}
}

// trimQuestion keeps a question small enough for a notification (the push
// service takes 4 KB): the text and options a lock screen can show.
func trimQuestion(q *sessionv1.Question) *sessionv1.Question {
	if q == nil {
		return nil
	}
	q = proto.Clone(q).(*sessionv1.Question)
	if len(q.Items) > 3 {
		q.Items = q.Items[:3]
	}
	for _, it := range q.Items {
		it.Text, _ = provider.Truncate(it.Text, 500)
		it.Header, _ = provider.Truncate(it.Header, 60)
		if len(it.Options) > 6 {
			it.Options = it.Options[:6]
		}
		for _, o := range it.Options {
			o.Label, _ = provider.Truncate(o.Label, 60)
		}
	}
	return q
}
