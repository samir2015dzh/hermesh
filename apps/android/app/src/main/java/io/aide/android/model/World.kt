package io.aide.android.model

import io.aide.android.api.Client
import io.aide.android.api.Device
import io.aide.android.api.GetStatusResponse
import io.aide.android.api.Machine
import io.aide.android.api.Project
import io.aide.android.api.Session
import io.aide.android.api.SessionCursor
import io.aide.android.api.SessionStatus
import io.aide.android.api.WatchResponse
import io.aide.android.push.LiveNotices
import java.time.Instant

/**
 * What this phone knows of the owner's world, as the core's Watch gives it
 * (docs/state-sync.md): the first message carries everything, then each
 * change. Sessions are merged, a later state winning, so older pages
 * already loaded stay.
 */
data class World(
    val status: GetStatusResponse = GetStatusResponse(),
    val machines: List<Machine> = emptyList(),
    val clients: List<Client> = emptyList(),
    val devices: List<Device> = emptyList(),
    val projects: List<Project> = emptyList(),
    val sessions: Map<String, Session> = emptyMap(),
    val next: SessionCursor? = null,
    /** The first message came. */
    val watched: Boolean = false,
) {
    fun apply(r: WatchResponse): World {
        val base = if (r.reset) World(sessions = sessions, watched = true, next = r.next) else this
        return base.copy(
            status = r.status ?: base.status,
            machines = upsert(base.machines, r.machines, r.removedMachineIds) { it.machineId }.sortedBy { it.machineName.lowercase() },
            clients = upsert(base.clients, r.clients, r.removedGrantIds) { it.grantId }.sortedBy { it.label.lowercase() },
            devices = upsert(base.devices, r.devices, r.removedDeviceKeyIds) { it.keyId },
            projects = upsert(base.projects, r.projects, r.removedProjects.map { "${it.machineId}/${it.projectId}" }) { "${it.machineId}/${it.projectId}" },
            sessions = mergeSessions(base.sessions, r.sessions, r.deletedSessionIds),
        )
    }

    fun machineName(id: String): String = machines.firstOrNull { it.machineId == id }?.machineName ?: id.take(12)

    /** Who sent a session's work, as the person knows them: "You", or the agent's grant name. */
    fun senderName(s: Session): String {
        val who = when {
            s.createdBy == "owner" -> "You"
            else -> clients.firstOrNull { it.clientId == s.createdBy }?.label
                ?: "Disconnected agent"
        }
        return if (s.createdAs.isNotEmpty() && s.createdAs != who) "$who · ${s.createdAs}" else who
    }

    /**
     * A session as screens show it: while its machine can't be read (offline, or not answering) its state is not
     * known, as it may have finished or asked something meanwhile, so it is neither at work nor waiting for the
     * person, and reads "state unknown" with when it last changed, until the machine is back (as packages/core
     * withKnownState).
     */
    fun shown(s: Session): Session =
        if (s.status == SessionStatus.ARCHIVED || stateKnown(s)) s
        else s.copy(status = SessionStatus.UNKNOWN, progress = s.progress?.copy(pendingQuestion = null))

    /** The limits sessions' states are worked out with. */
    val stateLimits: StateLimits get() = StateLimits.of(status)

    /**
     * Whether a session's state is known now: not while its machine can't be read, nor once it has not been heard
     * from for the lost limit (this phone lost the relay), as packages/core stateKnown.
     */
    fun stateKnown(s: Session, now: Instant = Instant.now()): Boolean {
        val m = machines.firstOrNull { it.machineId == s.machineId } ?: return true // not listed yet
        if (m.unreachable.isNotEmpty()) return false
        val link = sessionStateOf(s, Liveness.of(m), stateLimits, now).link
        return link != SessionLink.LOST && link != SessionLink.CHECKING
    }

    /** What a session is doing and how fresh that is, from what is last known of it (docs/session-states.md). */
    fun view(s: Session, now: Instant = Instant.now()): StateView =
        sessionStateOf(sessions[s.sessionId] ?: s, Liveness.of(machines.firstOrNull { it.machineId == s.machineId }), stateLimits, now)

    val buckets: Buckets get() = Buckets.of(sessions.values.map { shown(it) })
}

fun <T> upsert(list: List<T>, changed: List<T>, removed: List<String>, key: (T) -> String): List<T> {
    if (changed.isEmpty() && removed.isEmpty()) return list
    val gone = removed.toSet() + changed.map(key).toSet()
    return list.filter { key(it) !in gone } + changed.filter { key(it) !in removed.toSet() }
}

/** Sessions merged by id, the later updated_at winning, less those deleted. */
fun mergeSessions(held: Map<String, Session>, changed: List<Session>, deleted: List<String> = emptyList()): Map<String, Session> {
    if (changed.isEmpty() && deleted.isEmpty()) return held
    val out = held.toMutableMap()
    for (s in changed) {
        val old = out[s.sessionId]
        if (old == null || instant(s.updatedAt) >= instant(old.updatedAt)) out[s.sessionId] = s
    }
    deleted.forEach(out::remove)
    return out
}

fun instant(ts: String): Instant = if (ts.isEmpty()) Instant.EPOCH else runCatching { Instant.parse(ts) }.getOrDefault(Instant.EPOCH)

/**
 * The sessions as the menu lists them (newest first): those that need the person, those with a result not opened yet
 * (until it is opened, even while its machine is lost), those at work, the rest.
 */
data class Buckets(val needsYou: List<Session>, val unseen: List<Session>, val inProgress: List<Session>, val recent: List<Session>) {
    companion object {
        fun of(all: Collection<Session>): Buckets {
            val sorted = all.filter { it.status != SessionStatus.ARCHIVED }.sortedByDescending { instant(it.updatedAt) }
            val needs = sorted.filter { needsYou(it) }
            val news = sorted.filter { it !in needs && doneUnseen(it) }
            val working = sorted.filter { it !in needs && it !in news && it.status in setOf(SessionStatus.RUNNING, SessionStatus.PREPARING, SessionStatus.QUEUED) }
            val rest = sorted.filter { it !in needs && it !in news && it !in working }
            return Buckets(needs, news, working, rest)
        }
    }
}

/** Waiting for an answer, or stopped by a failure (as the desktop's needsAttention). */
fun needsYou(s: Session): Boolean = s.status == SessionStatus.WAITING_INPUT || s.progress?.pendingQuestion != null || LiveNotices.failed(s)

/** Finished well, its result not opened yet; one whose state is not known now stays with it. */
fun doneUnseen(s: Session): Boolean = s.status in setOf(SessionStatus.IDLE, SessionStatus.UNKNOWN) && s.progress?.lastFailure == null && unseen(s)

/** A session's title in lists: its own, else the first line of its last message, else its id. */
fun displayTitle(s: Session): String = s.title.ifBlank { s.progress?.lastMessageExcerpt?.lineSequence()?.firstOrNull()?.take(60).orEmpty() }.ifBlank { s.sessionId.take(12) }

fun isLive(s: Session): Boolean = s.status in setOf(SessionStatus.RUNNING, SessionStatus.PREPARING, SessionStatus.QUEUED)

/** The coding agents' names as people know them. */
fun agentTitle(id: String): String = when (id) {
    "claude" -> "Claude Code"
    "codex" -> "Codex"
    "gemini" -> "Gemini CLI"
    "deepseek" -> "DeepSeek"
    "cursor" -> "Cursor"
    "copilot" -> "Copilot"
    "opencode" -> "OpenCode"
    "qwen" -> "Qwen Code"
    "kimi" -> "Kimi"
    else -> id.replaceFirstChar { it.uppercase() }
}
