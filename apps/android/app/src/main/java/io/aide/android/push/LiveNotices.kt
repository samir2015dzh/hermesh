package io.aide.android.push

import io.aide.android.api.Machine
import io.aide.android.api.Notice
import io.aide.android.api.Question
import io.aide.android.api.Session
import io.aide.android.api.SessionStatus
import io.aide.android.model.World
import io.aide.android.model.displayTitle
import io.aide.android.model.instant
import io.aide.android.model.instantOrNull
import java.time.Duration
import java.time.Instant

/**
 * Notices from the core's own connection (docs/android-app.md §4): what
 * changed in the owner's world, as Watch brings it, said as the notices a
 * machine seals for a phone (the kinds in noticeKinds), so they show as
 * before (show, Push.notify). It follows the
 * desktop's rules (packages/core notices.ts):
 *
 * - A session seen for the first time is new when it was created after the
 *   first look and not by the owner (the owner just did); one seen before
 *   tells about a new failure. Finished turns are not asked for.
 * - A question goes to the owner's desktop first (docs/session-states.md
 *   §7, questionDue): the phone tells of it once no desktop said it told the
 *   owner within DESKTOP_GRACE of its asking, or one did but the owner was
 *   away and it is still unanswered ESCALATE_AFTER after; never once the
 *   owner was at a desktop while it waited. From a machine that keeps
 *   nothing seen (an older one), at once, as before.
 *
 * What is no longer pending (an answered question) is withdrawn.
 */
class LiveNotices {
    private data class Snapshot(val failed: Boolean)

    /** What to show, and the threads (notificationId) to take back. */
    data class Changes(val show: List<Notice>, val withdraw: List<String>)

    private var seen = mapOf<String, Snapshot>()
    private var since: Instant? = null
    private var asking = setOf<String>()
    /** Each pending question by its id: when this phone first saw it; and those it told of. */
    private var firstSeen = mapOf<String, Instant>()
    private var rang = setOf<String>()

    fun look(w: World, now: Instant = Instant.now()): Changes {
        val show = mutableListOf<Notice>()
        val withdraw = mutableListOf<String>()
        val status = w.status
        if (!status.initialized || status.removed) return Changes(show, withdraw)

        // Sessions.
        for (s in w.sessions.values) {
            val before = seen[s.sessionId]
            val q = pendingQuestion(s)
            if (before == null && since?.let { instant(s.createdAt) >= it } == true && s.createdBy != "owner") {
                show += notice(w, s, "session_started")
            }
            if (q != null && q.questionId !in rang) {
                val due = questionDue(s, q, firstSeen[q.questionId] ?: now)
                if (due != null && !now.isBefore(due)) {
                    show += notice(w, s, "question").copy(question = q)
                    rang = rang + q.questionId
                }
            }
            if (before != null && failed(s) && !before.failed) {
                show += notice(w, s, "failure").copy(failure = s.progress?.lastFailure?.kind.orEmpty(), failureMessage = s.progress?.lastFailure?.message.orEmpty())
            }
        }
        val nowAsking = w.sessions.values.filter { pendingQuestion(it) != null }.map { it.sessionId }.toSet()
        // A question answered here or elsewhere: its notification goes (a failure since replaced it).
        for (id in asking - nowAsking) if (show.none { it.sessionId == id }) withdraw += id
        asking = nowAsking
        val pending = w.sessions.values.mapNotNull { pendingQuestion(it)?.questionId }.toSet()
        firstSeen = pending.associateWith { firstSeen[it] ?: now }
        rang = rang intersect pending
        seen = w.sessions.mapValues { (_, s) -> Snapshot(failed(s)) }
        if (since == null) since = now

        return Changes(show, withdraw)
    }

    private fun notice(w: World, s: Session, kind: String) = Notice(
        kind = kind, sessionId = s.sessionId, title = displayTitle(s), agent = s.agent, project = s.projectId,
        machine = w.machineName(s.machineId),
        grant = w.clients.firstOrNull { it.clientId == s.createdBy }?.label.orEmpty(),
    )

    companion object {
        /** How long a desktop has to say it told the owner of a question before the phone does. */
        val DESKTOP_GRACE: Duration = Duration.ofSeconds(10)
        /** A question a desktop told of, the owner away, still unanswered this long after it was asked, rings here. */
        val ESCALATE_AFTER: Duration = Duration.ofMinutes(3)

        /**
         * When the phone tells of a question q of s it first saw at firstSeen, or null: never, the owner having been
         * at a desktop while it waited (Session.seen, kept by its machine; docs/session-states.md §7).
         */
        fun questionDue(s: Session, q: Question, firstSeen: Instant): Instant? {
            val seen = s.seen ?: return firstSeen // an older machine: at once, as before
            val asked = instantOrNull(q.askedAt) ?: firstSeen
            return when {
                seen.questionId != q.questionId -> asked + DESKTOP_GRACE
                seen.questionPresent -> null
                else -> asked + ESCALATE_AFTER
            }
        }


        /** The question a session waits on (as the desktop's pendingQuestion). */
        fun pendingQuestion(s: Session) = if (s.status == SessionStatus.WAITING_INPUT) s.progress?.pendingQuestion else null

        /** A session that failed (as the desktop's isFailed): crashed, or a turn's failure other than an interruption. */
        fun failed(s: Session): Boolean = s.status == SessionStatus.CRASHED || (
            s.status !in setOf(SessionStatus.WAITING_INPUT, SessionStatus.ARCHIVED, "", "SESSION_STATUS_UNSPECIFIED") &&
                s.progress?.lastFailure != null && s.progress.lastFailure.kind != "FAILURE_KIND_INTERRUPTED"
            )
    }
}
