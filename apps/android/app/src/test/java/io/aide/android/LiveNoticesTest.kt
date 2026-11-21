package io.aide.android

import io.aide.android.api.ConnectClient
import io.aide.android.api.WatchResponse
import io.aide.android.connection.Link
import io.aide.android.connection.link
import io.aide.android.model.World
import io.aide.android.push.LiveNotices
import io.aide.android.push.NoticeTarget
import io.aide.android.push.show
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import java.time.Instant
import java.util.Locale

/** Notices from the core's own connection: Watch messages (protojson, as the core sends them) in, notifications out. */
class LiveNoticesTest {
    private lateinit var locale: Locale

    @Before fun english() {
        locale = Locale.getDefault()
        Locale.setDefault(Locale.ENGLISH)
    }

    @After fun restore() = Locale.setDefault(locale)

    private fun watch(json: String) = ConnectClient.json.decodeFromString(WatchResponse.serializer(), json)

    private val first = """{"reset":true,
        "status":{"initialized":true,"ownerId":"o_1","relayConnected":true},
        "machines":[{"machineId":"m_1","machineName":"owner-mac","online":true,"her":{"enabled":true,"unread":1,
          "latest":{"botId":"b_1","botName":"Muse","text":"old","at":"2026-11-17T07:00:00Z","kind":"HER_LATEST_KIND_MESSAGE"}}}],
        "clients":[{"clientId":"c_1","grantId":"g_1","label":"Codex on laptop"},{"clientId":"c_her","grantId":"g_2","label":"her","herMachineId":"m_1"}],
        "sessions":[
          {"sessionId":"s_old","machineId":"m_1","projectId":"demo","agent":"codex","status":"SESSION_STATUS_IDLE","createdBy":"c_1","createdAt":"2026-11-17T07:00:00Z","updatedAt":"2026-11-17T07:00:00Z","title":"Old"},
          {"sessionId":"s_ask","machineId":"m_1","projectId":"demo","agent":"gemini","status":"SESSION_STATUS_WAITING_INPUT","createdBy":"c_1","createdAt":"2026-11-17T07:00:00Z","updatedAt":"2026-11-17T07:00:00Z","title":"Colors",
           "progress":{"pendingQuestion":{"questionId":"q1","items":[{"id":"i1","text":"Which color?"}]}}}
        ]}"""

    private val t0: Instant = Instant.parse("2026-11-17T08:00:00Z")

    @Test fun theFirstLook() {
        val live = LiveNotices()
        val c = live.look(World().apply(watch(first)), t0)
        // A question still waiting; older sessions and Her's old message are no news.
        assertEquals(listOf("question"), c.show.map { it.kind })
        val q = show(c.show[0])
        assertEquals("Gemini CLI asks · Colors", q.title)
        assertEquals("Which color?", q.body)
        assertEquals(NoticeTarget(session = "s_ask"), q.target)
        // Seen: nothing again.
        assertEquals(emptyList<Any>(), live.look(World().apply(watch(first)), t0).show)
    }

    @Test fun whatChanges() {
        val live = LiveNotices()
        var w = World().apply(watch(first))
        live.look(w, t0)
        w = w.apply(
            watch(
                """{"sessions":[
              {"sessionId":"s_new","machineId":"m_1","projectId":"site","agent":"claude","status":"SESSION_STATUS_QUEUED","createdBy":"c_1","createdAt":"2026-11-17T08:00:05Z","updatedAt":"2026-11-17T08:00:05Z","title":"Fix the header"},
              {"sessionId":"s_mine","machineId":"m_1","projectId":"site","agent":"claude","status":"SESSION_STATUS_QUEUED","createdBy":"owner","createdAt":"2026-11-17T08:00:05Z","updatedAt":"2026-11-17T08:00:05Z","title":"Mine"},
              {"sessionId":"s_her","machineId":"m_1","projectId":"site","agent":"claude","status":"SESSION_STATUS_QUEUED","createdBy":"c_her","createdAt":"2026-11-17T08:00:06Z","updatedAt":"2026-11-17T08:00:06Z","title":"Her's"},
              {"sessionId":"s_old","machineId":"m_1","projectId":"demo","agent":"codex","status":"SESSION_STATUS_CRASHED","createdBy":"c_1","createdAt":"2026-11-17T07:00:00Z","updatedAt":"2026-11-17T08:00:07Z","title":"Old",
               "progress":{"lastFailure":{"kind":"FAILURE_KIND_RATE_LIMIT","message":"429"}}},
              {"sessionId":"s_ask","machineId":"m_1","projectId":"demo","agent":"gemini","status":"SESSION_STATUS_RUNNING","createdBy":"c_1","createdAt":"2026-11-17T07:00:00Z","updatedAt":"2026-11-17T08:00:08Z","title":"Colors"}
            ],
            "machines":[{"machineId":"m_1","machineName":"owner-mac","online":true,"her":{"enabled":true,"unread":2,
              "latest":{"botId":"b_1","botName":"Muse","text":"Your card details, please","at":"2026-11-17T08:00:09Z","kind":"HER_LATEST_KIND_APPROVAL"}}}]}""",
            ),
        )
        val c = live.look(w, t0)
        // New sessions started by others (not the owner's own), a failure. A message of Her's, from a machine
        // whose Aide still has her, tells nothing: this app no longer has Her.
        assertEquals(listOf("failure", "session_started", "session_started"), c.show.map { it.kind })
        val shown = c.show.associate { (it.sessionId.ifEmpty { it.kind }) to show(it) }
        assertEquals(setOf("s_old", "s_new", "s_her"), shown.keys)
        assertEquals("Fix the header", shown["s_new"]!!.title)
        assertEquals("Codex on laptop started it · owner-mac\nsite · Claude Code", shown["s_new"]!!.body)
        assertEquals("her started it · owner-mac\nsite · Claude Code", shown["s_her"]!!.body)
        assertEquals("“Old” failed", shown["s_old"]!!.title)
        assertEquals("Rate limited · Codex on laptop · demo", shown["s_old"]!!.body)
        // The question answered elsewhere: its notification goes.
        assertEquals(listOf("s_ask"), c.withdraw)

        // A new question in a session seen before.
        w = w.apply(
            watch(
                """{"sessions":[{"sessionId":"s_new","machineId":"m_1","projectId":"site","agent":"claude","status":"SESSION_STATUS_WAITING_INPUT","createdBy":"c_1","createdAt":"2026-11-17T08:00:05Z","updatedAt":"2026-11-17T08:01:00Z","title":"Fix the header",
                 "progress":{"pendingQuestion":{"questionId":"q2","items":[{"id":"i1","text":"Blue or red?"}]}}}],
                "machines":[{"machineId":"m_1","machineName":"owner-mac","online":true,"her":{"enabled":true,"unread":0,
                  "latest":{"botId":"b_1","botName":"Muse","text":"Your card details, please","at":"2026-11-17T08:00:09Z","kind":"HER_LATEST_KIND_APPROVAL"}}}]}""",
            ),
        )
        val c2 = live.look(w, t0)
        assertEquals(listOf("question"), c2.show.map { it.kind })
        assertEquals("Claude Code asks · Fix the header", show(c2.show[0]).title)
        assertEquals(emptyList<String>(), c2.withdraw)
    }

    // A question goes to the owner's desktop first (docs/session-states.md §7): the phone tells of it once no desktop
    // said it told the owner within the grace; after three minutes when one did, the owner away; never once the owner
    // was at a desktop, or once it is answered.
    @Test fun questionsGoToTheDesktopFirst() {
        fun asking(seen: String, status: String = "SESSION_STATUS_WAITING_INPUT") = watch(
            """{"reset":true,"status":{"initialized":true},"sessions":[{"sessionId":"s_ask","machineId":"m_1","agent":"gemini","status":"$status","createdBy":"c_1","title":"Colors",
              "seen":$seen,"progress":{"pendingQuestion":{"questionId":"q1","askedAt":"2026-11-17T08:00:00Z","items":[{"id":"i1","text":"Which color?"}]}}}]}""",
        )
        fun kinds(live: LiveNotices, w: WatchResponse, after: Long) = live.look(World().apply(w), t0.plusSeconds(after)).show.map { it.kind }

        // No desktop told of it: once the grace is over.
        var live = LiveNotices()
        assertEquals(emptyList<String>(), kinds(live, asking("{}"), 0))
        assertEquals(emptyList<String>(), kinds(live, asking("{}"), 9))
        assertEquals(listOf("question"), kinds(live, asking("{}"), 10))
        assertEquals(emptyList<String>(), kinds(live, asking("{}"), 200)) // once

        // A desktop told of it, the owner away: three minutes after it was asked.
        val told = """{"questionId":"q1"}"""
        live = LiveNotices()
        assertEquals(emptyList<String>(), kinds(live, asking(told), 0))
        assertEquals(emptyList<String>(), kinds(live, asking(told), 179))
        assertEquals(listOf("question"), kinds(live, asking(told), 180))

        // The owner at a desktop while it waited: never here.
        live = LiveNotices()
        kinds(live, asking(told), 0)
        assertEquals(emptyList<String>(), kinds(live, asking("""{"questionId":"q1","questionPresent":true}"""), 600))

        // Answered before the three minutes: nothing (the session runs on).
        live = LiveNotices()
        kinds(live, asking(told), 0)
        assertEquals(emptyList<String>(), kinds(live, asking(told, "SESSION_STATUS_RUNNING"), 60))
        assertEquals(emptyList<String>(), kinds(live, asking(told, "SESSION_STATUS_RUNNING"), 400))
    }

    @Test fun mutedAndInterrupted() {
        val live = LiveNotices()
        var w = World().apply(watch(first))
        live.look(w, t0)
        w = w.apply(
            watch(
                """{"sessions":[{"sessionId":"s_old","machineId":"m_1","projectId":"demo","agent":"codex","status":"SESSION_STATUS_IDLE","createdBy":"c_1","createdAt":"2026-11-17T07:00:00Z","updatedAt":"2026-11-17T08:00:07Z","title":"Old",
                 "progress":{"lastFailure":{"kind":"FAILURE_KIND_INTERRUPTED"}}}],
                "machines":[{"machineId":"m_1","machineName":"owner-mac","online":true,"her":{"enabled":true,"unread":3,
                  "latest":{"botId":"b_1","botName":"Muse","text":"psst","at":"2026-11-17T08:00:09Z","muted":true}}}]}""",
            ),
        )
        assertEquals(emptyList<Any>(), live.look(w, t0).show)
    }

    @Test fun notSetUpOrSignedOut() {
        val live = LiveNotices()
        fun asking(status: String) = watch(
            """{"reset":true,"status":{$status},"sessions":[{"sessionId":"s_ask","machineId":"m_1","status":"SESSION_STATUS_WAITING_INPUT","createdBy":"c_1",
              "progress":{"pendingQuestion":{"questionId":"q1","items":[{"id":"i1","text":"Which color?"}]}}}]}""",
        )
        // Before this phone logged in, and once another device signed it out: nothing.
        assertEquals(emptyList<Any>(), live.look(World().apply(asking(""""initialized":false""")), t0).show)
        assertEquals(emptyList<Any>(), live.look(World().apply(asking(""""initialized":true,"removed":true""")), t0).show)
        assertEquals(listOf("question"), live.look(World().apply(watch(first)), t0).show.map { it.kind })
    }

    @Test fun theLinkAsShown() {
        assertEquals(Link.Connected, link(running = true, online = true, relayConnected = true))
        assertEquals(Link.Reconnecting, link(running = true, online = true, relayConnected = false))
        assertEquals(Link.Disconnected, link(running = true, online = false, relayConnected = false))
        // Paused (in the background with the switch off): what the core last said is not current.
        assertEquals(Link.Disconnected, link(running = false, online = true, relayConnected = true))
    }
}
