package io.aide.android

import io.aide.android.api.Client
import io.aide.android.api.Failure
import io.aide.android.api.Machine
import io.aide.android.api.Question
import io.aide.android.api.QuestionItem
import io.aide.android.api.QuestionOption
import io.aide.android.api.Session
import io.aide.android.api.SessionProgress
import io.aide.android.api.SessionSeen
import io.aide.android.api.SessionStatus
import io.aide.android.api.WatchResponse
import io.aide.android.api.Answer
import io.aide.android.api.ApiError
import io.aide.android.api.Device
import io.aide.android.model.World
import io.aide.android.model.buildAnswers
import io.aide.android.model.choose
import io.aide.android.model.loginError
import io.aide.android.model.newLoginProblem
import io.aide.android.model.openedNews
import io.aide.android.model.plainHttp
import io.aide.android.model.serverURL
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.Locale

class WorldTest {
    private fun s(id: String, status: String, at: String, by: String = "c_1") = Session(sessionId = id, status = status, updatedAt = at, createdBy = by)

    @Test fun resetReplacesAllButSessions() {
        var w = World().apply(WatchResponse(reset = true, machines = listOf(Machine("m_1", "b")), devices = listOf(Device("k_1", "Pixel"), Device("k_2", "Mac")),
            sessions = listOf(s("old", SessionStatus.IDLE, "2026-01-01T00:00:00Z"))))
        w = w.apply(WatchResponse(machines = listOf(Machine("m_2", "a")), removedDeviceKeyIds = listOf("k_2")))
        assertEquals(listOf("a", "b"), w.machines.map { it.machineName })
        assertEquals(listOf("k_1"), w.devices.map { it.keyId })
        // Again from the start: the machines it lists alone, the sessions loaded kept.
        w = w.apply(WatchResponse(reset = true, machines = listOf(Machine("m_2", "a")), sessions = listOf(s("new", SessionStatus.RUNNING, "2026-01-02T00:00:00Z"))))
        assertEquals(listOf("m_2"), w.machines.map { it.machineId })
        assertEquals(setOf("old", "new"), w.sessions.keys)
        w = w.apply(WatchResponse(deletedSessionIds = listOf("old")))
        assertEquals(setOf("new"), w.sessions.keys)
    }

    @Test fun aLaterStateWins() {
        var w = World().apply(WatchResponse(sessions = listOf(s("x", SessionStatus.RUNNING, "2026-01-02T00:00:00.5Z"))))
        w = w.apply(WatchResponse(sessions = listOf(s("x", SessionStatus.IDLE, "2026-01-02T00:00:00.25Z")))) // older (shorter fraction)
        assertEquals(SessionStatus.RUNNING, w.sessions["x"]!!.status)
        w = w.apply(WatchResponse(sessions = listOf(s("x", SessionStatus.IDLE, "2026-01-02T00:00:01Z"))))
        assertEquals(SessionStatus.IDLE, w.sessions["x"]!!.status)
    }

    @Test fun bucketsAndWhoSentIt() {
        val asking = s("q", SessionStatus.RUNNING, "2026-01-01T00:00:03Z").copy(progress = SessionProgress(pendingQuestion = Question("q1")))
        val w = World(
            clients = listOf(Client(clientId = "c_1", label = "Muse"), Client(clientId = "c_her", label = "her", herMachineId = "m_1")),
            sessions = listOf(
                asking, s("r", SessionStatus.RUNNING, "2026-01-01T00:00:02Z"), s("i", SessionStatus.IDLE, "2026-01-01T00:00:04Z"),
                s("a", SessionStatus.ARCHIVED, "2026-01-01T00:00:05Z"), s("w", SessionStatus.WAITING_INPUT, "2026-01-01T00:00:01Z", by = "owner"),
            ).associateBy { it.sessionId },
        )
        val b = w.buckets
        assertEquals(listOf("q", "w"), b.needsYou.map { it.sessionId })
        assertEquals(listOf("r"), b.inProgress.map { it.sessionId })
        assertEquals(listOf("i"), b.recent.map { it.sessionId })
        assertEquals("Muse", w.senderName(asking))
        assertEquals("Muse · reviewer", w.senderName(asking.copy(createdAs = "reviewer")))
        // A grant Her held, from before Aide dropped her: named as its grant is.
        assertEquals("her", w.senderName(asking.copy(createdBy = "c_her")))
        assertEquals("You", w.senderName(asking.copy(createdBy = "owner")))
        assertEquals("Disconnected agent", w.senderName(asking.copy(createdBy = "c_gone")))
    }

    // Needs you (a question, a failure), new results not opened yet, at work, the rest; an unopened result stays
    // with the new ones while its machine is away, and leaves them once opened.
    @Test fun unopenedResults() {
        val news = s("n", SessionStatus.IDLE, "2026-01-01T00:00:04Z").copy(progress = SessionProgress(turnCount = 2), seen = SessionSeen(turns = 1))
        val opened = s("o", SessionStatus.IDLE, "2026-01-01T00:00:03Z").copy(progress = SessionProgress(turnCount = 2), seen = SessionSeen(turns = 2))
        val failed = s("f", SessionStatus.IDLE, "2026-01-01T00:00:02Z").copy(progress = SessionProgress(turnCount = 2, lastFailure = Failure(kind = "FAILURE_KIND_NETWORK")), seen = SessionSeen(turns = 1))
        val running = s("r", SessionStatus.RUNNING, "2026-01-01T00:00:01Z").copy(progress = SessionProgress(turnCount = 2), seen = SessionSeen(turns = 1))
        val w = World(sessions = listOf(news, opened, failed, running).associateBy { it.sessionId })
        val b = w.buckets
        assertEquals(listOf("f"), b.needsYou.map { it.sessionId })
        assertEquals(listOf("n"), b.unseen.map { it.sessionId })
        assertEquals(listOf("r"), b.inProgress.map { it.sessionId })
        assertEquals(listOf("o"), b.recent.map { it.sessionId })
        assertEquals("2/", openedNews(news))
        assertEquals("", openedNews(opened))
        val away = World(machines = listOf(Machine("m_vps", "vps", unreachable = "offline")), sessions = mapOf("n" to news.copy(machineId = "m_vps")))
        assertEquals(listOf("n"), away.buckets.unseen.map { it.sessionId })
        assertEquals("", openedNews(away.shown(away.sessions["n"]!!)))
        val seen = w.copy(sessions = w.sessions + ("n" to news.copy(seen = SessionSeen(turns = 2))))
        assertEquals(setOf("o", "n"), seen.buckets.recent.map { it.sessionId }.toSet())
    }

    // A session whose machine can't be read is neither at work nor asking: its state is unknown, its time kept,
    // until the machine is back.
    @Test fun stateUnknownWhileItsMachineIsAway() {
        val running = s("r", SessionStatus.RUNNING, "2026-01-01T00:00:02Z").copy(machineId = "m_vps")
        val asking = s("q", SessionStatus.WAITING_INPUT, "2026-01-01T00:00:03Z").copy(machineId = "m_vps", progress = SessionProgress(pendingQuestion = Question("q1")))
        val archived = s("a", SessionStatus.ARCHIVED, "2026-01-01T00:00:04Z").copy(machineId = "m_vps")
        val away = World(machines = listOf(Machine("m_vps", "vps", unreachable = "offline")), sessions = listOf(running, asking, archived).associateBy { it.sessionId })
        val b = away.buckets
        assertEquals(emptyList<String>(), b.needsYou.map { it.sessionId } + b.inProgress.map { it.sessionId })
        assertEquals(listOf(SessionStatus.UNKNOWN, SessionStatus.UNKNOWN), b.recent.map { it.status })
        assertEquals("2026-01-01T00:00:02Z", away.shown(running).updatedAt)
        assertEquals(archived, away.shown(archived))
        val back = away.copy(machines = listOf(Machine("m_vps", "vps", online = true)))
        assertEquals(running, back.shown(running))
        assertEquals(listOf("r"), back.buckets.inProgress.map { it.sessionId })
    }
}

class LinksTest {
    @Test fun serverAddresses() {
        assertEquals("https://mesh.example.com", serverURL("mesh.example.com/"))
        assertEquals("http://203.0.113.7:8080", serverURL("203.0.113.7:8080"))
        assertEquals("http://relay.lan:8080", serverURL("http://relay.lan:8080"))
        assertNull(serverURL("  "))
    }
}

/** The account: what a new one needs, before it is sent, and what failures say. */
class AccountTest {
    private lateinit var locale: Locale

    @org.junit.Before fun english() {
        locale = Locale.getDefault()
        Locale.setDefault(Locale.ENGLISH)
    }

    @org.junit.After fun restore() = Locale.setDefault(locale)

    @Test fun aNewLoginAsTheServerChecksIt() {
        // relay.CheckNewLogin: an account without spaces or slashes, at most 64; a password of 8 or more, not the account.
        assertNull(newLoginProblem(" Owner ", "correct horse"))
        assertNotNull(newLoginProblem("", "correct horse"))
        assertNotNull(newLoginProblem("the owner", "correct horse"))
        assertNotNull(newLoginProblem("a/b", "correct horse"))
        assertNotNull(newLoginProblem("a".repeat(65), "correct horse"))
        assertEquals("The password needs at least 8 characters.", newLoginProblem("owner", "1234567"))
        assertNull(newLoginProblem("owner", "12345678"))
        assertNull(newLoginProblem("owner", "密码密码密码密码")) // characters, not bytes
        assertNotNull(newLoginProblem("password1", " PASSWORD1 "))
    }

    @Test fun plainHttpIsWarnedOf() {
        assertTrue(plainHttp("http://aide.lan:8080"))
        assertTrue(plainHttp("203.0.113.7:8080")) // an IP address: http (no certificate)
        assertFalse(plainHttp("aide.example.com"))
        assertFalse(plainHttp("https://aide.example.com"))
        assertFalse(plainHttp(""))
    }

    @Test fun failuresAsThePersonReadsThem() {
        assertEquals("Wrong account or password.", loginError(ApiError("permission_denied", "wrong account or password")))
        assertEquals("Wrong setup code.", loginError(ApiError("permission_denied", "wrong setup code")))
        assertEquals("The current password is wrong.", loginError(ApiError("permission_denied", "the current password is wrong")))
        assertTrue(loginError(ApiError("resource_exhausted", "too many wrong passwords; try again in 2 minutes")).startsWith("Too many tries"))
        assertTrue(loginError(ApiError("failed_precondition", "logging in is turned off for this account; turn it on from a device that is logged in")).startsWith("Logging in is turned off"))
        assertTrue(loginError(ApiError("already_exists", "this server is set up already: log in with its account")).startsWith("This server is set up already"))
        assertEquals("the password needs at least 8 characters", loginError(ApiError("invalid_argument", "the password needs at least 8 characters")))
    }
}

class AnswersTest {
    private val pick = QuestionItem(id = "color", text = "Which?", options = listOf(QuestionOption("红"), QuestionOption("蓝")))
    private val many = QuestionItem(id = "tags", options = listOf(QuestionOption("a"), QuestionOption("b")), multiSelect = true)
    private val note = QuestionItem(id = "note", optional = true)

    @Test fun answersNeedEveryRequiredItem() {
        assertNull(buildAnswers(listOf(pick, note), emptyMap(), emptyMap()))
        assertEquals(listOf(Answer("color", listOf("红"))), buildAnswers(listOf(pick, note), mapOf("color" to setOf("红")), emptyMap()))
        assertEquals(
            listOf(Answer("color", emptyList(), "绿"), Answer("note", emptyList(), "hi")),
            buildAnswers(listOf(pick, note), emptyMap(), mapOf("color" to " 绿 ", "note" to "hi")),
        )
        // Options keep their order, whatever order they were tapped in.
        assertEquals(listOf(Answer("tags", listOf("a", "b"))), buildAnswers(listOf(many), mapOf("tags" to setOf("b", "a")), emptyMap()))
    }

    @Test fun choosing() {
        assertEquals(setOf("蓝"), choose(pick, setOf("红"), "蓝"))
        assertEquals(emptySet<String>(), choose(pick, setOf("红"), "红"))
        assertEquals(setOf("a", "b"), choose(many, setOf("a"), "b"))
        assertEquals(setOf("a"), choose(many, setOf("a", "b"), "b"))
    }
}
