@file:OptIn(ExperimentalSerializationApi::class)

package io.aide.android.api

import kotlinx.serialization.EncodeDefault
import kotlinx.serialization.ExperimentalSerializationApi
import kotlinx.serialization.KSerializer
import kotlinx.serialization.Serializable
import kotlinx.serialization.descriptors.PrimitiveKind
import kotlinx.serialization.descriptors.PrimitiveSerialDescriptor
import kotlinx.serialization.encoding.Decoder
import kotlinx.serialization.encoding.Encoder
import kotlinx.serialization.json.JsonDecoder
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.longOrNull

// The aide.local.v1 messages the app reads and sends, as protojson has
// them: lowerCamelCase names, 64-bit integers as strings, enums by name,
// timestamps as RFC 3339, bytes as base64, defaults left out. Only the
// fields the app uses; the rest are ignored. A request's field whose
// default is not proto's (zero) is sent always (@EncodeDefault).

/** A 64-bit integer, a string in protojson (a number is taken too). */
object Int64 : KSerializer<Long> {
    override val descriptor = PrimitiveSerialDescriptor("Int64", PrimitiveKind.STRING)
    override fun serialize(encoder: Encoder, value: Long) = encoder.encodeString(value.toString())
    override fun deserialize(decoder: Decoder): Long {
        if (decoder is JsonDecoder) {
            val p = decoder.decodeJsonElement() as? JsonPrimitive ?: return 0
            return p.longOrNull ?: p.content.toLongOrNull() ?: 0
        }
        return decoder.decodeString().toLongOrNull() ?: 0
    }
}

@Serializable
data class GetStatusResponse(
    val initialized: Boolean = false,
    val ownerId: String = "",
    val relayUrl: String = "",
    val relayConnected: Boolean = false,
    val version: String = "",
    val hosted: Boolean = false,
    val deviceKeyId: String = "",
    val removed: Boolean = false,
    val signedOut: Boolean = false,
    val account: String = "",
    val mcpUrl: String = "",
    val abilities: List<String> = emptyList(),
    /** The limits sessions' states are worked out with (docs/session-states.md). */
    val stateLimits: StateLimits? = null,
)

@Serializable
data class StateLimits(val lostAfterSeconds: Int = 0, val stallAfterSeconds: Int = 0)

/** A time a machine could not be read for at least the lost limit; to is "" while it lasts. */
@Serializable
data class LostSpan(val from: String = "", val to: String = "")

@Serializable
data class AgentStatus(
    val name: String = "",
    val available: Boolean = false,
    /** The owner hid it (SetAgentHidden): left out of the machine's list, as on the desktop. */
    val hidden: Boolean = false,
    val version: String = "",
    val loggedIn: Boolean = false,
    val detail: String = "",
    val checking: Boolean = false,
    /** The models it offers here, to pick a contact's from. */
    val models: List<AgentModel> = emptyList(),
    /** Its per-session modes (permission presets) here. */
    val modes: List<AgentMode> = emptyList(),
)

@Serializable
data class AgentModel(val id: String = "", val name: String = "", val efforts: List<String> = emptyList())

@Serializable
data class AgentMode(val id: String = "", val name: String = "")

@Serializable
data class HerStatus(
    val enabled: Boolean = false,
    val running: Boolean = false,
    val version: String = "",
    val modelReady: Boolean = false,
    val unsupported: String = "",
    val unread: Int = 0,
    val latest: HerLatest? = null,
    val error: String = "",
    /** Her was on there once and her data is still there. */
    val data: Boolean = false,
)

/** An assistant's newest message, for a notification (HerStatus.latest). */
@Serializable
data class HerLatest(
    val botId: String = "",
    val botName: String = "",
    val text: String = "",
    val at: String = "",
    /** HER_LATEST_KIND_MESSAGE, _QUESTION or _APPROVAL (a secure card waits). */
    val kind: String = "",
    val muted: Boolean = false,
)

@Serializable
data class MachineStatus(
    val name: String = "",
    val platform: String = "",
    val agents: List<AgentStatus> = emptyList(),
    val runningSessions: Int = 0,
    val maxSessions: Int = 0,
    val paused: Boolean = false,
    val version: String = "",
)

@Serializable
data class Machine(
    val machineId: String = "",
    val machineName: String = "",
    val online: Boolean = false,
    val lastSeen: String = "",
    val status: MachineStatus? = null,
    val local: Boolean = false,
    val deviceKeyId: String = "",
    val version: String = "",
    val unreachable: String = "",
    val syncing: Boolean = false,
    val her: HerStatus? = null,
    /** When this phone last knew it was there, once it no longer is. */
    val heardAt: String = "",
    val unreachableSince: String = "",
    val lostSpans: List<LostSpan> = emptyList(),
)

@Serializable
data class GrantSpec(
    val label: String = "",
    val machineIds: List<String> = emptyList(),
    val projectIds: List<String> = emptyList(),
    val agents: List<String> = emptyList(),
    val taskVisibility: String = "",
    val ttlDays: Int = 0,
    val maxConcurrentSessions: Int = 0,
)

@Serializable
data class Client(
    val clientId: String = "",
    val grantId: String = "",
    val label: String = "",
    val fingerprint: String = "",
    val spec: GrantSpec? = null,
    val expiresAt: String = "",
    val online: Boolean = false,
    val lastSeen: String = "",
    val revoked: Boolean = false,
    val custodian: String = "",
    val herMachineId: String = "",
)

@Serializable
data class Device(
    val keyId: String = "",
    val label: String = "",
    val platform: String = "",
    val thisDevice: Boolean = false,
    val machineId: String = "",
    val machineOnline: Boolean = false,
    /** Set up before accounts: a root key of its own in the root cert. */
    val legacy: Boolean = false,
)

@Serializable
data class Project(
    val machineId: String = "",
    val projectId: String = "",
    val description: String = "",
    val path: String = "",
    /** The coding agents that may work in it; empty: all. */
    val agents: List<String> = emptyList(),
)

@Serializable
data class ProjectRef(val machineId: String = "", val projectId: String = "")

@Serializable
data class Failure(val kind: String = "", val message: String = "", val retryable: Boolean = false, val hint: String = "")

@Serializable
data class QuestionOption(val label: String = "", val description: String = "")

@Serializable
data class QuestionItem(
    val id: String = "",
    val header: String = "",
    val text: String = "",
    val options: List<QuestionOption> = emptyList(),
    val multiSelect: Boolean = false,
    val allowOther: Boolean = false,
    val secret: Boolean = false,
    val optional: Boolean = false,
)

@Serializable
data class Question(
    val questionId: String = "",
    val items: List<QuestionItem> = emptyList(),
    val askedAt: String = "",
    val expiresAt: String = "",
)

@Serializable
data class Answer(val itemId: String = "", val selected: List<String> = emptyList(), val text: String = "")

@Serializable
data class SessionProgress(
    val phase: String = "",
    val lastMessageExcerpt: String = "",
    val currentTool: String = "",
    val lastFailure: Failure? = null,
    val pendingQuestion: Question? = null,
    val turnStartedAt: String = "",
    /** When its last event was recorded. */
    val lastEventAt: String = "",
    /** Turns finished. */
    val turnCount: Int = 0,
)

/** What the owner has seen of a session, kept by its machine (MarkSessionSeen). */
@Serializable
data class SessionSeen(
    val turns: Int = 0,
    /** The pending question a desktop told the owner of, and whether the owner was at a desktop while it waited. */
    val questionId: String = "",
    val questionPresent: Boolean = false,
)

object SessionStatus {
    const val QUEUED = "SESSION_STATUS_QUEUED"
    const val PREPARING = "SESSION_STATUS_PREPARING"
    const val RUNNING = "SESSION_STATUS_RUNNING"
    const val IDLE = "SESSION_STATUS_IDLE"
    const val ARCHIVED = "SESSION_STATUS_ARCHIVED"
    const val CRASHED = "SESSION_STATUS_CRASHED"
    const val WAITING_INPUT = "SESSION_STATUS_WAITING_INPUT"
    /** Not known now: its machine can't be read (World.shown). */
    const val UNKNOWN = "SESSION_STATUS_UNSPECIFIED"
}

@Serializable
data class Session(
    val sessionId: String = "",
    val machineId: String = "",
    val projectId: String = "",
    val agent: String = "",
    val status: String = "",
    val createdBy: String = "",
    val progress: SessionProgress? = null,
    val createdAt: String = "",
    val updatedAt: String = "",
    val error: String = "",
    val branch: String = "",
    val result: String = "",
    val title: String = "",
    val createdAs: String = "",
    val model: String = "",
    val effort: String = "",
    /** Unset from an older machine, which keeps nothing seen. */
    val seen: SessionSeen? = null,
)

@Serializable
data class SessionCursor(val updatedAt: String = "", val sessionId: String = "")

@Serializable
data class WatchResponse(
    val reset: Boolean = false,
    val status: GetStatusResponse? = null,
    val machines: List<Machine> = emptyList(),
    val removedMachineIds: List<String> = emptyList(),
    val clients: List<Client> = emptyList(),
    val removedGrantIds: List<String> = emptyList(),
    val devices: List<Device> = emptyList(),
    val removedDeviceKeyIds: List<String> = emptyList(),
    val projects: List<Project> = emptyList(),
    val removedProjects: List<ProjectRef> = emptyList(),
    val sessions: List<Session> = emptyList(),
    val deletedSessionIds: List<String> = emptyList(),
    val next: SessionCursor? = null,
)

// Session events (aide.session.v1.SessionEvent): one of the kinds is set.

@Serializable data class StatusChanged(val status: String = "", val reason: String = "")
@Serializable data class TurnStarted(val asker: String = "")
@Serializable data class AttachmentInfo(val name: String = "", val mediaType: String = "", val path: String = "", val image: Boolean = false)
@Serializable data class UserMessage(val text: String = "", val attachments: List<AttachmentInfo> = emptyList(), val from: String = "", val fromName: String = "")
@Serializable data class AgentMessage(val text: String = "")
@Serializable data class AgentDraft(val text: String = "", val whole: Boolean = false)
/** title: the line it shows by when summary is its raw arguments (a session read from DSH's or Claude Desktop's records). */
@Serializable data class ToolCall(val callId: String = "", val kind: String = "", val name: String = "", val summary: String = "", val decision: String = "", val reason: String = "", val title: String = "")
@Serializable data class ToolResult(val callId: String = "", val summary: String = "", val isError: Boolean = false, val truncated: Boolean = false, val preview: Boolean = false)
@Serializable data class TurnCompleted(val result: String = "", val isError: Boolean = false, val commit: String = "", val failure: Failure? = null)
@Serializable data class ErrorEvent(val message: String = "", val failure: Failure? = null)
@Serializable data class QuestionAsked(val question: Question? = null)
@Serializable data class QuestionResolved(val questionId: String = "", val resolution: String = "", val by: String = "", val answers: List<Answer> = emptyList())
@Serializable data class MessageReceived(val messageId: String = "", val from: String = "", val fromName: String = "", val text: String = "")
@Serializable data class RolledBack(val turn: Int = 0, val lastTurn: Int = 0, val by: String = "")
@Serializable data class MessageSent(val messageId: String = "", val to: String = "", val toName: String = "", val text: String = "")
/** A managed workspace's request as it stands after one change (docs/workspace-requests.md); from: "agent" or a client id. */
@Serializable data class WorkspaceRequest(
    val requestId: String = "", val kind: String = "", val status: String = "", val from: String = "", val fromName: String = "",
    val item: String = "", val note: String = "",
)

@Serializable
data class SessionEvent(
    @Serializable(with = Int64::class) val seq: Long = 0,
    val ts: String = "",
    val turnId: String = "",
    val status: StatusChanged? = null,
    val turnStarted: TurnStarted? = null,
    val userMessage: UserMessage? = null,
    val agentMessage: AgentMessage? = null,
    val toolCall: ToolCall? = null,
    val toolResult: ToolResult? = null,
    val turnCompleted: TurnCompleted? = null,
    val error: ErrorEvent? = null,
    val questionAsked: QuestionAsked? = null,
    val questionResolved: QuestionResolved? = null,
    val agentDraft: AgentDraft? = null,
    val messageReceived: MessageReceived? = null,
    val messageSent: MessageSent? = null,
    val workspaceRequest: WorkspaceRequest? = null,
    val rolledBack: RolledBack? = null,
)

// Requests and the answers the app reads.

@Serializable data class Empty(val unused: String? = null)
/** Setting a new server up with the setup code it printed, and the account and password it will keep (docs/accounts.md). */
@Serializable data class SetupRequest(
    val relayUrl: String,
    val setupCode: String,
    val account: String,
    val password: String,
    val label: String,
    @EncodeDefault val platform: String = "android",
)
@Serializable data class SetupResponse(val ownerId: String = "")
/** Logging in to the owner's server with the account and password: the server signs this phone in at once. */
@Serializable data class LogInRequest(
    val relayUrl: String,
    val account: String,
    val password: String,
    val label: String,
    @EncodeDefault val platform: String = "android",
)
@Serializable data class LogInResponse(val ownerId: String = "")
/** The account, as the server keeps it; off: logging in is turned off; convert: an old password login waits to be converted. */
@Serializable data class GetLoginResponse(val account: String = "", val server: String = "", val convert: Boolean = false, val off: Boolean = false)
/** A new name or password, or logging in off or on, with the current password; account and newPassword empty: kept. */
@Serializable data class SetLoginRequest(val currentPassword: String, val account: String = "", val newPassword: String = "", val off: Boolean = false)
/** How many devices and agents a new password signed out. */
@Serializable data class SetLoginResponse(val signedOut: Int = 0)
@Serializable data class ListDevicesResponse(val devices: List<Device> = emptyList())
@Serializable data class RemoveDeviceRequest(val keyId: String, val eraseHer: Boolean = false)
@Serializable data class RemoveDeviceResponse(val reissuedMachines: Int = 0, val reissuedGrants: Int = 0)
@Serializable data class SessionIdRequest(val sessionId: String)
@Serializable data class SessionResponse(val session: Session? = null)
@Serializable data class StreamSessionEventsRequest(
    val sessionId: String,
    @Serializable(with = Int64::class) val afterSeq: Long = 0,
    @EncodeDefault val follow: Boolean = true,
    @EncodeDefault val view: String = "EVENT_VIEW_BASIC",
    @EncodeDefault val partial: Boolean = true,
)
@Serializable data class AnswerQuestionRequest(val sessionId: String, val questionId: String, val answers: List<Answer> = emptyList(), val decline: Boolean = false)
@Serializable data class MarkSessionSeenRequest(val sessionId: String, val opened: Boolean = false, val questionId: String = "", val present: Boolean = false)
@Serializable data class AnswerQuestionResponse(val session: Session? = null)
@Serializable data class MachineIdRequest(val machineId: String)
/** DeepSeek Harness's own sessions on the owner's machines (docs: aide.machine.v1.DSHSession); takeover: the owner took it over in Aide. */
@Serializable data class DSHSession(
    val sessionId: String = "",
    val title: String = "",
    val cwd: String = "",
    val state: String = "",
    val createdAt: String = "",
    val lastActivityAt: String = "",
    val turnOpen: Boolean = false,
    val archived: Boolean = false,
    val agentPreset: String = "",
    val error: String = "",
    val takeover: Boolean = false,
)
/** The Aide bridge plugin in DSH Desktop (aide.machine.v1.DSHBridge). */
@Serializable data class DSHBridge(val state: String = "", val version: String = "", val detail: String = "")
/** dshHome: the DSH home that machine reads DSH's sessions from. */
@Serializable data class DSHBridgeEntry(val machineId: String = "", val bridge: DSHBridge = DSHBridge(), val dshHome: String = "")
@Serializable data class SetDSHTakeoverRequest(val machineId: String, val sessionId: String, val on: Boolean)
@Serializable data class SetDSHTakeoverResponse(val session: DSHSession = DSHSession())
@Serializable data class SendDSHMessageRequest(val machineId: String, val sessionId: String, val text: String, val requestId: String)
@Serializable data class SendDSHMessageResponse(val requestId: String = "")
@Serializable data class InterruptDSHSessionRequest(val machineId: String, val sessionId: String)
@Serializable data class InterruptDSHSessionResponse(val interrupted: Boolean = false)
/** lost: its machine can't be reached, and the session is as last read (readAt): its state last known, nothing can be sent. */
@Serializable data class DSHSessionEntry(val machineId: String = "", val machineName: String = "", val session: DSHSession = DSHSession(), val lost: Boolean = false, val readAt: String = "")
@Serializable data class DSHUnreachable(val machineId: String = "", val machineName: String = "")
@Serializable data class ListDSHSessionsResponse(val sessions: List<DSHSessionEntry> = emptyList(), val unreachable: List<DSHUnreachable> = emptyList(), val bridges: List<DSHBridgeEntry> = emptyList())
@Serializable data class ReadDSHSessionRequest(
    val machineId: String,
    val sessionId: String,
    @Serializable(with = Int64::class) val beforeSeq: Long = 0,
    @EncodeDefault val limit: Int = 200,
    @EncodeDefault val view: String = "EVENT_VIEW_FULL",
)
@Serializable data class ReadDSHSessionResponse(val session: DSHSession = DSHSession(), val events: List<SessionEvent> = emptyList(), val hasMoreBefore: Boolean = false)
@Serializable data class ListSessionsRequest(@EncodeDefault val limit: Int = 50, val before: SessionCursor? = null, @EncodeDefault val view: String = "SESSION_VIEW_BASIC")
@Serializable data class ListSessionsResponse(val sessions: List<Session> = emptyList(), val next: SessionCursor? = null)


@Serializable data class DiffStat(val filesChanged: Int = 0, val insertions: Int = 0, val deletions: Int = 0)

/** aide.session.v1.Notice: what a machine sealed to this phone's notice key. */
@Serializable
data class Notice(
    val kind: String = "",
    val sessionId: String = "",
    val title: String = "",
    val grant: String = "",
    val agent: String = "",
    val project: String = "",
    val machine: String = "",
    val question: Question? = null,
    val failure: String = "",
    val failureMessage: String = "",
    val turn: Int = 0,
    val diffStat: DiffStat? = null,
    val herKind: String = "",
    val machineId: String = "",
    val botId: String = "",
    val botName: String = "",
    val text: String = "",
)

// Contacts (docs/personas.md): aide.identity.v1.Persona and LocalService's calls for them.

@Serializable
data class Persona(
    val personaId: String = "",
    val name: String = "",
    val icon: String = "",
    val color: String = "",
    val machineId: String = "",
    val projectId: String = "",
    val agent: String = "",
    val model: String = "",
    val effort: String = "",
    val mode: String = "",
    /** The session it talks in now; "" before its first message or once its presets changed. */
    val sessionId: String = "",
)

@Serializable data class ListPersonasResponse(val personas: List<Persona> = emptyList())
@Serializable data class SavePersonaRequest(val persona: Persona)
@Serializable data class SavePersonaResponse(val persona: Persona? = null)
@Serializable data class DeletePersonaRequest(val personaId: String)
@Serializable data class TalkToPersonaRequest(val personaId: String, val text: String, val idempotencyKey: String = "")

@Serializable
data class TalkToPersonaResponse(
    val session: Session? = null,
    /** A new session was started; else the message went to the one it talks in. */
    val started: Boolean = false,
    /** Why it was started: first, archived, gone, changed. */
    val reason: String = "",
    val persona: Persona? = null,
)
