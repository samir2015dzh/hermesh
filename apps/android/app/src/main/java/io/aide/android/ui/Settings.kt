package io.aide.android.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import io.aide.android.api.Device
import io.aide.android.api.GetLoginResponse
import io.aide.android.api.SetLoginRequest
import io.aide.android.model.AppModel
import io.aide.android.model.MIN_PASSWORD
import io.aide.android.model.newLoginProblem
import io.aide.android.model.normalizeAccount
import kotlinx.coroutines.launch

/**
 * Settings: the account the server keeps (docs/accounts.md §4: its name
 * and password, changed with the current password, and logging in turned
 * off or on), the owner's devices (any other one can be signed out), and
 * this phone.
 */
@Composable
fun SettingsTab(model: AppModel) {
    val world by model.world.collectAsState()
    val st = world.status
    val keep by model.keepalive.enabled.collectAsState()
    var login by remember { mutableStateOf<GetLoginResponse?>(null) }
    // Bumped after a change: the account is read again.
    var version by remember { mutableIntStateOf(0) }
    LaunchedEffect(version) {
        login = model.login()
        if (version == 0) model.refreshDevices()
    }
    var editing by remember { mutableStateOf<AccountChange?>(null) }
    var signingOut by remember { mutableStateOf<Device?>(null) }
    Column(Modifier.fillMaxSize()) {
        PageTitle(t("Settings", "设置"))
        SyncLine(model)
        LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            item { SectionHeader(t("Account", "账号")) }
            item {
                Column(Modifier.fillMaxWidth().aideCard().padding(14.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    val l = login
                    Labeled(t("Account", "账号"), l?.account?.ifEmpty { null } ?: st.account)
                    Labeled(t("Server", "服务器"), l?.server?.ifEmpty { null } ?: st.relayUrl)
                    Text(
                        t("Log in on a new device, computer or agent with this server, account and password.", "在新的设备、电脑或 Agent 上，用这个服务器、账号和密码登录。"),
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                    if (l != null && l.convert) {
                        Text(
                            t("Your password login from before accounts waits to be converted: type its password once.", "账号功能之前的密码登录还没有转换：输入一次它的密码即可。"),
                            style = MaterialTheme.typography.bodySmall, color = Warning,
                        )
                        Button(onClick = { editing = AccountChange.Convert }) { Text(t("Convert…", "转换…")) }
                    }
                    if (l != null && !l.convert) {
                        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                            Column(Modifier.weight(1f)) {
                                Text(t("Logging in", "允许登录"), style = MaterialTheme.typography.bodyMedium)
                                Text(
                                    if (l.off) {
                                        t("Off: no new device, machine or agent can log in. Those logged in stay.", "已关闭：新的设备、机器或 Agent 都无法登录，已登录的不受影响。")
                                    } else {
                                        t("On: whatever has the account and password can log in.", "已开启：知道账号和密码就能登录。")
                                    },
                                    style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                                )
                            }
                            Spacer(Modifier.width(12.dp))
                            Switch(checked = !l.off, onCheckedChange = { editing = AccountChange.Logins(off = !it) })
                        }
                        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            TextButton(onClick = { editing = AccountChange.Name }) { Text(t("Change account name…", "修改账号…")) }
                            TextButton(onClick = { editing = AccountChange.Password }) { Text(t("Change password…", "修改密码…")) }
                        }
                    }
                }
            }

            item { SectionHeader(t("Devices", "设备")) }
            items(world.devices, key = { it.keyId }) { d -> DeviceRow(d, signOut = { signingOut = d }) }

            item {
                SectionHeader(t("This phone", "这台手机"))
                Column(Modifier.fillMaxWidth().aideCard().padding(horizontal = 14.dp, vertical = 6.dp)) {
                    Labeled(t("Server", "服务器"), st.relayUrl)
                    if (st.account.isNotEmpty()) Labeled(t("Account", "账号"), st.account)
                    Labeled(t("Core version", "核心版本"), st.version)
                    KeepaliveSettings(model)
                    Labeled(
                        t("Notifications", "通知"),
                        if (keep) t("through Aide’s own connection", "经 Aide 自己的连接") else t("while Aide is open", "Aide 打开时"),
                    )
                }
            }
        }
    }
    editing?.let { c ->
        val l = login
        if (l != null) AccountDialog(model, c, l, done = { changed -> editing = null; if (changed) version++ })
    }
    signingOut?.let { d -> SignOutDialog(model, d, world.machines.firstOrNull { it.machineId == d.machineId }?.her?.let { it.enabled || it.data } == true) { signingOut = null } }
}

@Composable
private fun DeviceRow(d: Device, signOut: () -> Unit) {
    Row(Modifier.fillMaxWidth().padding(horizontal = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(d.label + if (d.thisDevice) t(" (this phone)", "（这台手机）") else "", fontWeight = if (d.thisDevice) FontWeight.SemiBold else FontWeight.Normal)
            Text(
                listOfNotNull(
                    d.platform.takeIf { it.isNotEmpty() },
                    if (d.machineId.isNotEmpty()) (if (d.machineOnline) t("its machine online", "它的机器在线") else t("its machine offline", "它的机器离线")) else null,
                    if (d.legacy) t("set up before accounts", "账号功能之前设置") else null,
                ).joinToString(" · "),
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
        // Any device signs out another, never itself.
        if (!d.thisDevice) TextButton(onClick = signOut) { Text(t("Sign out…", "登出…")) }
    }
}

/** What the account dialog changes; each needs the current password. */
private sealed interface AccountChange {
    data object Name : AccountChange
    data object Password : AccountChange
    data class Logins(val off: Boolean) : AccountChange
    data object Convert : AccountChange
}

@Composable
private fun AccountDialog(model: AppModel, c: AccountChange, login: GetLoginResponse, done: (changed: Boolean) -> Unit) {
    var current by remember { mutableStateOf("") }
    var name by remember { mutableStateOf(login.account) }
    var fresh by remember { mutableStateOf("") }
    var again by remember { mutableStateOf("") }
    var error by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    AlertDialog(
        onDismissRequest = { if (!busy) done(false) },
        title = {
            Text(
                when (c) {
                    AccountChange.Name -> t("Change the account name", "修改账号")
                    AccountChange.Password -> t("Change the password", "修改密码")
                    is AccountChange.Logins -> if (c.off) t("Turn logging in off", "关闭登录") else t("Turn logging in on", "开启登录")
                    AccountChange.Convert -> t("Convert the password login", "转换密码登录")
                },
            )
        },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                when (c) {
                    AccountChange.Name -> {
                        OutlinedTextField(name, { name = it; error = null }, label = { Text(t("New account name", "新账号")) }, singleLine = true)
                        Text(t("Every device, computer and agent logs in with it from now on.", "以后所有设备、电脑和 Agent 都用它登录。"), style = MaterialTheme.typography.bodySmall)
                    }
                    AccountChange.Password -> {
                        Text(
                            t(
                                "A new password signs out your other devices and every agent that logged in: they log in again with it. This phone and your machines stay.",
                                "新密码会登出你的其他设备和所有已登录的 Agent，它们需要用新密码重新登录。这台手机和你的机器不受影响。",
                            ),
                            style = MaterialTheme.typography.bodySmall, color = Warning,
                        )
                        PasswordField(fresh, { fresh = it; error = null }, t("New password", "新密码"), hint = passwordHint)
                        PasswordField(again, { again = it; error = null }, t("New password again", "再次输入新密码"))
                    }
                    is AccountChange.Logins -> Text(
                        if (c.off) {
                            t("No new device, machine or agent can log in until you turn it on again. Those logged in stay.", "在你重新开启之前，新的设备、机器或 Agent 都无法登录；已登录的不受影响。")
                        } else {
                            t("Whatever has the account and password can log in again.", "知道账号和密码就又可以登录了。")
                        },
                        style = MaterialTheme.typography.bodySmall,
                    )
                    AccountChange.Convert -> Text(
                        t("The server keeps the account from now on; devices and agents approved before keep working.", "从此由服务器保存账号；之前批准的设备和 Agent 照常可用。"),
                        style = MaterialTheme.typography.bodySmall,
                    )
                }
                PasswordField(current, { current = it; error = null }, t("Current password", "当前密码"))
                error?.let { Text(it, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall) }
            }
        },
        confirmButton = {
            Button(
                enabled = !busy && current.isNotEmpty() && when (c) {
                    AccountChange.Name -> name.isNotBlank() && normalizeAccount(name) != login.account
                    AccountChange.Password -> fresh.isNotEmpty() && again.isNotEmpty()
                    else -> true
                },
                onClick = {
                    val req = when (c) {
                        AccountChange.Name -> {
                            newLoginProblem(name, "x".repeat(MIN_PASSWORD))?.let { error = it; return@Button }
                            SetLoginRequest(current, account = name.trim(), off = login.off)
                        }
                        AccountChange.Password -> {
                            if (fresh != again) {
                                error = t("The two new passwords differ.", "两次输入的新密码不一样。")
                                return@Button
                            }
                            newLoginProblem(login.account, fresh)?.let { error = it; return@Button }
                            SetLoginRequest(current, newPassword = fresh, off = login.off)
                        }
                        is AccountChange.Logins -> SetLoginRequest(current, off = c.off)
                        AccountChange.Convert -> SetLoginRequest(current, off = login.off)
                    }
                    busy = true
                    scope.launch {
                        val signedOut = model.setLogin(req) { error = it }
                        busy = false
                        if (signedOut == null) return@launch
                        model.say(
                            when (c) {
                                AccountChange.Password -> t("Password changed; $signedOut devices and agents signed out", "密码已修改，登出了 $signedOut 台设备和 Agent")
                                AccountChange.Name -> t("Account name changed", "账号已修改")
                                is AccountChange.Logins -> if (c.off) t("Logging in is off", "登录已关闭") else t("Logging in is on", "登录已开启")
                                AccountChange.Convert -> t("Converted", "已转换")
                            },
                        )
                        done(true)
                    }
                },
            ) { Text(t("Change", "修改")) }
        },
        dismissButton = { TextButton(enabled = !busy, onClick = { done(false) }) { Text(t("Cancel", "取消")) } },
    )
}

@Composable
private fun PasswordField(value: String, onChange: (String) -> Unit, label: String, hint: (@Composable () -> Unit)? = null) {
    OutlinedTextField(
        value, onChange, label = { Text(label) }, singleLine = true,
        visualTransformation = PasswordVisualTransformation(),
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
        supportingText = hint,
    )
}

/** Signing another device out: it can no longer connect, and its own machine is removed (RemoveDevice). */
@Composable
private fun SignOutDialog(model: AppModel, d: Device, herDataOnItsMachine: Boolean, done: () -> Unit) {
    var eraseHer by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    AlertDialog(
        onDismissRequest = { if (!busy) done() },
        title = { Text(t("Sign out “${d.label}”?", "登出“${d.label}”？")) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(
                    t(
                        "It can no longer connect" + (if (d.machineId.isNotEmpty()) ", and its own machine is removed" else "") + ". It can log in again with the account and password.",
                        "它将无法再连接" + (if (d.machineId.isNotEmpty()) "，它自己的机器也会被移除" else "") + "。之后它可以用账号和密码重新登录。",
                    ),
                )
                // Data Her left there, from before Aide dropped her: it can go with the machine.
                if (herDataOnItsMachine) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Checkbox(eraseHer, { eraseHer = it })
                        Text(t("Also erase Her’s data on its machine", "同时清除它机器上 Her 的数据"), style = MaterialTheme.typography.bodyMedium)
                    }
                }
            }
        },
        confirmButton = {
            Button(enabled = !busy, onClick = {
                busy = true
                scope.launch {
                    if (model.removeDevice(d, eraseHer)) {
                        model.say(t("“${d.label}” signed out", "已登出“${d.label}”"))
                        done()
                    } else {
                        busy = false
                    }
                }
            }) { Text(t("Sign Out", "登出")) }
        },
        dismissButton = { TextButton(enabled = !busy, onClick = done) { Text(t("Cancel", "取消")) } },
    )
}
