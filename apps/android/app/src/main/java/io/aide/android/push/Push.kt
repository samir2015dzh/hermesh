package io.aide.android.push

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import io.aide.android.MainActivity
import io.aide.android.R
import io.aide.android.ui.t

/**
 * Notifications the app shows itself: the notices its own connection
 * brings (LiveNotices, docs/android-app.md §4), one a thread, a tap opening
 * what it is about.
 */
object Push {
    private const val CHANNEL = "notices"

    /** A notice shown (one from the core's connection: LiveNotices); newer ones of its thread replace it. */
    fun notify(context: Context, s: Shown) {
        if (ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) return
        val nm = context.getSystemService(NotificationManager::class.java) ?: return
        nm.createNotificationChannel(NotificationChannel(CHANNEL, t("Needs you", "需要你处理"), NotificationManager.IMPORTANCE_HIGH))
        val id = notificationId(s.thread)
        val open = Intent(context, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP
            putExtra(EXTRA_SESSION, s.target.session)
        }
        val n = NotificationCompat.Builder(context, CHANNEL)
            .setSmallIcon(R.drawable.ic_stat_aide)
            .setContentTitle(s.title)
            .setContentText(s.body)
            .setStyle(NotificationCompat.BigTextStyle().bigText(s.body))
            .setPriority(if (s.urgent) NotificationCompat.PRIORITY_HIGH else NotificationCompat.PRIORITY_DEFAULT)
            .setAutoCancel(true)
            .setContentIntent(PendingIntent.getActivity(context, id, open, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT))
            // On a locked screen, the generic words alone.
            .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
            .setPublicVersion(
                NotificationCompat.Builder(context, CHANNEL)
                    .setSmallIcon(R.drawable.ic_stat_aide)
                    .setContentTitle("Aide")
                    .setContentText(t("Something needs you. Open Aide to see it.", "有事需要你处理，打开 Aide 查看。"))
                    .build(),
            )
            .build()
        runCatching { nm.notify(id, n) }
    }

    /** A thread's notification taken back: what it was about is no longer pending. */
    fun withdraw(context: Context, thread: String) {
        context.getSystemService(NotificationManager::class.java)?.cancel(notificationId(thread))
    }

    const val EXTRA_SESSION = "io.aide.session"

    /** What a local notification's tap opens. */
    fun target(intent: Intent?): NoticeTarget? {
        intent ?: return null
        val t = NoticeTarget(session = intent.getStringExtra(EXTRA_SESSION).orEmpty())
        return t.takeUnless { it.isEmpty }
    }
}
