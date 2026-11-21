package io.aide.android.push

import io.aide.android.api.ConnectClient
import io.aide.android.api.Notice
import io.aide.android.model.agentTitle
import io.aide.android.ui.t

// What the phone shows of a notice (session.v1.Notice) its own connection
// brought (LiveNotices), as the iPhone's NoticeText says it.

/** The notices this phone asks for (as the iPhone's defaults). */
val noticeKinds = listOf("session_started", "question", "failure")

/** A notice opened, as the app's protojson reads it. */
fun parseNotice(json: String): Notice = ConnectClient.json.decodeFromString(Notice.serializer(), json)

/** What a notice opens: a session. */
data class NoticeTarget(val session: String = "") {
    val isEmpty get() = session.isEmpty()
}

/** A notice as the phone shows it: its words, the thread newer ones replace, what a tap opens. */
data class Shown(val title: String, val body: String, val thread: String, val target: NoticeTarget, val urgent: Boolean)

fun show(n: Notice): Shown = when (n.kind) {
    "question" -> Shown(
        t("${agentTitle(n.agent)} asks", "${agentTitle(n.agent)} 在提问") + (n.title.takeIf { it.isNotEmpty() }?.let { " · $it" } ?: ""),
        n.question?.items.orEmpty().joinToString("\n") { it.text },
        n.sessionId, NoticeTarget(session = n.sessionId), urgent = true,
    )
    "failure" -> Shown(
        t("“${n.title}” failed", "“${n.title}”失败了"),
        listOf(failureTitle(n.failure), n.grant, n.project).filter { it.isNotEmpty() }.joinToString(" · "),
        n.sessionId, NoticeTarget(session = n.sessionId), urgent = false,
    )
    "turn_done" -> Shown(
        t("“${n.title}”: turn ${n.turn} done", "“${n.title}”：第 ${n.turn} 轮完成"),
        n.diffStat?.takeIf { it.filesChanged > 0 }?.let { t("${it.filesChanged} files +${it.insertions} −${it.deletions}", "${it.filesChanged} 个文件 +${it.insertions} −${it.deletions}") }
            ?: t("No file changes", "没有文件改动"),
        n.sessionId, NoticeTarget(session = n.sessionId), urgent = false,
    )
    "session_started" -> Shown(
        n.title,
        listOf(
            if (n.grant.isEmpty()) n.machine else t("${n.grant} started it · ${n.machine}", "${n.grant} 发起 · ${n.machine}"),
            listOf(n.project, agentTitle(n.agent).takeIf { n.agent.isNotEmpty() }.orEmpty()).filter { it.isNotEmpty() }.joinToString(" · "),
        ).filter { it.isNotEmpty() }.joinToString("\n"),
        n.sessionId, NoticeTarget(session = n.sessionId), urgent = false,
    )
    else -> Shown("Aide", t("Something needs you. Open Aide to see it.", "有事需要你处理，打开 Aide 查看。"), n.kind, NoticeTarget(), urgent = false)
}

private fun failureTitle(kind: String): String = when (kind) {
    "FAILURE_KIND_AUTH" -> t("Not signed in", "未登录")
    "FAILURE_KIND_QUOTA" -> t("Out of quota", "额度已用完")
    "FAILURE_KIND_RATE_LIMIT" -> t("Rate limited", "被限流")
    "FAILURE_KIND_PROVIDER_ERROR" -> t("Model service error", "模型服务出错")
    "FAILURE_KIND_NETWORK" -> t("Network error", "网络错误")
    "FAILURE_KIND_CONTEXT_OVERFLOW" -> t("Context is full", "上下文已满")
    "FAILURE_KIND_MODEL_UNAVAILABLE" -> t("Model unavailable", "模型不可用")
    "FAILURE_KIND_AGENT_CRASHED" -> t("The agent quit unexpectedly", "Agent 意外退出")
    "FAILURE_KIND_TIMEOUT" -> t("Took too long", "耗时过长")
    else -> t("Failed", "失败")
}

/** A thread's notification id: newer notices of a thread replace older ones. */
fun notificationId(thread: String): Int = thread.hashCode() and 0x7fffffff
