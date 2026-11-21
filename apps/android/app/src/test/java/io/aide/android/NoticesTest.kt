package io.aide.android

import io.aide.android.api.ConnectClient
import io.aide.android.push.NoticeTarget
import io.aide.android.push.notificationId
import io.aide.android.push.parseNotice
import io.aide.android.push.show
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import java.util.Locale

/** The notices the phone shows, as it words them (as the iPhone's NoticeText). */
class NoticesTest {
    private lateinit var locale: Locale

    @Before fun english() {
        locale = Locale.getDefault()
        Locale.setDefault(Locale.ENGLISH)
    }

    @After fun restore() = Locale.setDefault(locale)

    // protojson as Go writes a Notice (cmd/aide-mobile/android OpenNotice).
    @Test fun question() {
        val n = parseNotice(
            """{"kind":"question","at":"2026-11-17T08:00:00Z","sessionId":"s_1","title":"选颜色","grant":"Muse","agent":"gemini","project":"demo","machine":"owner-mac",
              "question":{"questionId":"q1","items":[{"id":"i1","text":"Which color?","options":[{"label":"red"},{"label":"blue"}]},{"id":"i2","text":"Why?"}]},"unknownLater":1}""",
        )
        val s = show(n)
        assertEquals("Gemini CLI asks · 选颜色", s.title)
        assertEquals("Which color?\nWhy?", s.body)
        assertEquals(NoticeTarget(session = "s_1"), s.target)
        assertEquals("s_1", s.thread)
        assertTrue(s.urgent)
    }

    @Test fun inChinese() {
        Locale.setDefault(Locale.SIMPLIFIED_CHINESE)
        val s = show(parseNotice("""{"kind":"failure","sessionId":"s_2","title":"限流","grant":"Muse","project":"demo","failure":"FAILURE_KIND_RATE_LIMIT"}"""))
        assertEquals("“限流”失败了", s.title)
        assertEquals("被限流 · Muse · demo", s.body)
    }

    @Test fun otherKinds() {
        val started = show(parseNotice("""{"kind":"session_started","sessionId":"s_3","title":"Fix the build","grant":"Muse","machine":"mac","project":"demo","agent":"claude"}"""))
        assertEquals("Fix the build", started.title)
        assertEquals("Muse started it · mac\ndemo · Claude Code", started.body)

        val done = show(parseNotice("""{"kind":"turn_done","sessionId":"s_3","title":"Fix","turn":2,"diffStat":{"filesChanged":3,"insertions":10,"deletions":4}}"""))
        assertEquals("“Fix”: turn 2 done", done.title)
        assertEquals("3 files +10 −4", done.body)
        assertEquals("No file changes", show(parseNotice("""{"kind":"turn_done","title":"Fix","turn":1}""")).body)

        // A notice of Her's, from a machine whose Aide still has her: nothing here opens it.
        assertTrue(show(parseNotice("""{"kind":"her","herKind":"approval","machineId":"m_1","botId":"b1","botName":"Her","text":"Send the email?"}""")).target.isEmpty)

        val unknown = show(parseNotice("""{"kind":"something_new"}"""))
        assertTrue(unknown.target.isEmpty)
        // A request to connect, from a machine older than accounts: no such notice any more.
        assertTrue(show(parseNotice("""{"kind":"pairing","pairingLabel":"iPad","verificationCode":"ABCD-1234"}""")).target.isEmpty)
    }

    @Test fun oneNotificationPerThread() {
        assertEquals(notificationId("s_1"), notificationId("s_1"))
        assertTrue(notificationId("s_1") != notificationId("s_2"))
        assertTrue(notificationId("her:m_1:b1") >= 0)
    }
}
