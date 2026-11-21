package io.aide.android.ui

import androidx.compose.animation.togetherWith
import androidx.compose.animation.slideOutHorizontally
import androidx.compose.animation.slideInHorizontally
import androidx.compose.animation.fadeOut
import androidx.compose.animation.fadeIn
import androidx.compose.animation.core.tween
import androidx.compose.animation.Crossfade
import androidx.compose.animation.AnimatedContent
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.Home
import androidx.compose.material.icons.automirrored.filled.List
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.Person
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import io.aide.android.connection.Link
import io.aide.android.model.AppModel

/** A page over the tabs. */
sealed interface Route {
    data class Session(val id: String) : Route
    /** DeepSeek Harness's own sessions, and one of them. */
    data object DSH : Route
    data class DSHSession(val entry: io.aide.android.api.DSHSessionEntry) : Route
    /** Handing work over (docs/personas.md): pick a contact, then write to it. */
    data object Contacts : Route
    data class Contact(val id: String) : Route
    data class EditContact(val persona: io.aide.android.api.Persona) : Route
}

enum class Tab { Sessions, NeedsYou, Machines, Settings }

/** The page shown over the tabs (none: the tabs), and how deep in the stack, so a transition knows forward from back. */
private data class PageAt(val route: Route?, val depth: Int)

@Composable
fun Root(model: AppModel) {
    val phase by model.phase.collectAsState()
    val snackbar = remember { SnackbarHostState() }
    LaunchedEffect(Unit) { model.messages.collect { snackbar.showSnackbar(it) } }
    Box(Modifier.fillMaxSize()) {
        when (phase) {
            AppModel.Phase.Starting -> Centered { CircularProgressIndicator() }
            AppModel.Phase.Failed -> Centered { Text(t("Aide could not start: ", "Aide 无法启动：") + model.startError) }
            AppModel.Phase.Onboarding -> Onboarding(model, snackbar)
            AppModel.Phase.Ready -> {
                Home(model, snackbar)
                BackgroundGuide(model)
            }
        }
    }
}

@Composable
private fun Home(model: AppModel, snackbar: SnackbarHostState) {
    val world by model.world.collectAsState()
    var tab by rememberSaveable { mutableStateOf(Tab.Sessions) }
    val stack = remember { mutableStateListOf<Route>() }
    BackHandler(enabled = stack.isNotEmpty()) { stack.removeAt(stack.lastIndex) }
    // A notification tapped: what it is about.
    val opening by model.opening.collectAsState()
    LaunchedEffect(opening) {
        val o = opening ?: return@LaunchedEffect
        if (o.session.isNotEmpty()) { stack.clear(); stack.add(Route.Session(o.session)) }
        model.opened()
    }
    // A page over the tabs slides in a little from the side and fades, back the other way (Motion, as the desktop's).
    AnimatedContent(
        targetState = PageAt(stack.lastOrNull(), stack.size),
        transitionSpec = {
            val forward = targetState.depth > initialState.depth
            val shift = { w: Int -> if (forward) w / 6 else -w / 6 }
            (slideInHorizontally(tween(Motion.Long, easing = Motion.EaseOut), shift) + fadeIn(tween(Motion.Medium, easing = Motion.EaseOut)))
                .togetherWith(slideOutHorizontally(tween(Motion.Long, easing = Motion.EaseOut)) { w -> -shift(w) / 2 } + fadeOut(tween(Motion.Short)))
        },
        contentKey = { it.depth to it.route },
        label = "page",
    ) { at ->
    val top = at.route
    if (top != null) {
        when (top) {
            is Route.Session -> SessionPage(model, top.id, back = { stack.removeAt(stack.lastIndex) })
            Route.DSH -> DSHListPage(model, open = { stack.add(Route.DSHSession(it)) }, back = { stack.removeAt(stack.lastIndex) })
            is Route.DSHSession -> DSHSessionPage(model, top.entry, back = { stack.removeAt(stack.lastIndex) })
            Route.Contacts -> ContactsPage(model, open = { stack.add(Route.Contact(it.personaId)) }, add = { stack.add(Route.EditContact(io.aide.android.api.Persona(icon = "robot", color = io.aide.android.model.personaColors.first()))) }, back = { stack.removeAt(stack.lastIndex) })
            // Written to, the real session opens over the contact.
            is Route.Contact -> ContactPage(model, top.id, openSession = { stack.add(Route.Session(it)) }, edit = { stack.add(Route.EditContact(it)) }, back = { stack.removeAt(stack.lastIndex) })
            is Route.EditContact -> ContactEditPage(model, top.persona) { kept ->
                stack.removeAt(stack.lastIndex)
                // Deleted: its page goes too.
                if (kept == null && top.persona.personaId.isNotEmpty()) stack.removeAll { it is Route.Contact && it.id == top.persona.personaId }
                if (kept != null && top.persona.personaId.isEmpty()) stack.add(Route.Contact(kept.personaId))
            }
        }
        return@AnimatedContent
    }
    val needs = world.buckets.needsYou.size
    Scaffold(
        snackbarHost = { SnackbarHost(snackbar) },
        // Handing work over: a contact first, then what to do.
        floatingActionButton = {
            if (tab == Tab.Sessions) ExtendedFloatingActionButton(onClick = { stack.add(Route.Contacts) }, icon = { Icon(Icons.Filled.Person, null) }, text = { Text(t("Hand Work", "派活")) })
        },
        bottomBar = {
            NavigationBar {
                NavigationBarItem(tab == Tab.Sessions, { tab = Tab.Sessions }, icon = { Icon(Icons.AutoMirrored.Filled.List, null) }, label = { Text(t("Tasks", "任务")) })
                // Needs you: the sessions waiting for an answer, in one place.
                NavigationBarItem(tab == Tab.NeedsYou, { tab = Tab.NeedsYou }, icon = {
                    BadgedBox(badge = { if (needs > 0) Badge(containerColor = Warning, contentColor = androidx.compose.ui.graphics.Color.White) { Text("$needs") } }) { Icon(Icons.Filled.Notifications, null) }
                }, label = { Text(t("Needs you", "需要你")) })
                NavigationBarItem(tab == Tab.Machines, { tab = Tab.Machines }, icon = { Icon(Icons.Filled.Home, null) }, label = { Text(t("Machines", "机器")) })
                NavigationBarItem(tab == Tab.Settings, { tab = Tab.Settings }, icon = { Icon(Icons.Filled.Settings, null) }, label = { Text(t("Settings", "设置")) })
            }
        },
    ) { pad ->
        Box(Modifier.padding(pad)) {
            Crossfade(tab, animationSpec = tween(Motion.Medium, easing = Motion.EaseOut), label = "tab") { shown ->
                when (shown) {
                    Tab.Sessions -> SessionsTab(model, openDSH = { stack.add(Route.DSH) }) { stack.add(Route.Session(it)) }
                    Tab.NeedsYou -> NeedsYouTab(model) { stack.add(Route.Session(it)) }
                    Tab.Machines -> MachinesTab(model)
                    Tab.Settings -> SettingsTab(model)
                }
            }
        }
    }
    }
}

@Composable
fun Centered(content: @Composable () -> Unit) {
    Column(Modifier.fillMaxSize().padding(24.dp), verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
        content()
    }
}

/** The quiet line under a page's title while what shows is not current. */
@Composable
fun SyncLine(model: AppModel) {
    val world by model.world.collectAsState()
    val link by model.link.collectAsState()
    val text = when {
        !world.watched -> t("Connecting to Aide…", "正在连接 Aide…")
        link == Link.Disconnected -> t("Disconnected: no network", "已断开：没有网络")
        link == Link.Reconnecting -> t("Can’t reach the relay: reconnecting…", "无法连接中继：重连中…")
        world.machines.any { it.syncing } -> t("Updating…", "正在更新…")
        else -> return
    }
    Text(text, style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp))
}
