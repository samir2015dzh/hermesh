# Accounts: the server keeps the account, and signs whoever logs in

> Status: implemented (2026-11-20, the owner's rulings of that day: "账号密码改
> relay 直管", "彻底删除核对码与可信设备准入机制", "agent 行动审批也去掉").
> It replaces pairing codes, verification codes and digits, approval by
> another device, the signer (Touch ID, Windows Hello, the phone's
> Keystore) and the password key of docs/password-login.md.

In one sentence: **the owner's server keeps one account and password, and a
key of its own; whatever logs in with the account and password (a device, a
machine, an agent) is signed in by the server at once**, with no other device
approving and nothing to compare or confirm.

## 1. What changed in the trust model

Before, the root of trust was the owner's devices: each held a root key in
its secure hardware, listed in the root cert, and every credential (a
machine's, an agent's grant, a device's) was signed by one of them after the
owner confirmed it there. The relay was untrusted: it could deny service and
see metadata, nothing more.

Now **the server is the authority**, by the owner's choice (a self-hosted,
one-person setup, the way AA works):

- **The server's key** is a P-256 key the relay makes when it is set up (or
  when it converts an old password login, §5), kept in its data, and listed
  in the owner's root cert (`platform = "server"`, label 服务器). For an
  owner set up after this change it is the only root key, and the owner id
  is named by it.
- **Logging in** (`RelayPublicService.LogIn`): the person gives the server,
  the account and the password; the client sends them over the server's
  TLS with what logs in (`Joiner`: a device key, a machine key or an agent
  key, its name and platform). For the right password the server signs:
  - a device: an `OwnerDeviceCert` for its device key and, if it runs a
    machine, that machine's `MachineCert` (its `device_key_id` names the
    device);
  - a machine: a `MachineCert`;
  - an agent: a `Grant` to every machine, project and coding agent, its own
    tasks, until revoked (`relay.DefaultGrant`).
  The client checks what it got: a root chain that verifies under the owner
  it names, and a credential for its own key (`machine.CheckLogIn`).
- **Signing for the owner** (`OwnerControlService.Sign`): a device that
  logged in is the owner, so the server signs for it what adding or
  managing a machine or an agent takes: a `MachineCert` (this computer as a
  machine), a `Grant` (an agent's scope changed, renewed, renamed), an
  `AdminCommand` (Her on, a command connector), an `EraseOrder`. It signs
  only those types, only for its own owner; root certs it changes itself
  (§4). The device checks that what came back verifies and is what it
  asked (`owner.signRoot`).
- **Verification is unchanged.** Machines, agents and devices verify
  credentials against the root chain exactly as before: the server's key is
  a root key like any. End-to-end TLS between the two ends is unchanged:
  the relay forwards a tunnel's bytes and cannot read them.

The two "inviolable principles" of docs/technical-design.md §2.2 no longer
hold, by that choice: the server holds an authority that can make a machine
run anything (it can sign itself an agent's grant or a device cert), and it
keeps a secret (its key, and the account's password hash). §7 says what that
costs.

## 2. Setting a server up

A new server prints a **setup code** on its first start (and
`aide-relay setup-code` prints it again). A code is taken for an hour from
when it was made (`--setup-code-ttl`, `AIDE_RELAY_SETUP_CODE_TTL`; `0`:
forever): then SetUp refuses it ("the setup code expired"), and the next
one asked for (`aide-relay setup-code`, or the server's next start) is a
new one, the old one never taken again. A code kept by a server from before
codes expired gets its hour when next asked for. A server set up has no
code: its owner, devices, machines and agents are untouched. The first device sets it up
(`RelayPublicService.SetUp`; the apps' 设置新的服务器): the server, the setup
code, the account and the password it will keep. The server then makes its
key, signs root cert v1 listing it alone, keeps the account as an Argon2id
hash (t=3, 64 MiB, 4 lanes, 16-byte salt), forgets the setup code, and logs
that device in as LogIn would. A second setup is refused: the server is set
up.

Accounts are compared lowercase and trimmed; at most 64 characters, no
spaces or slashes; passwords have at least 8 characters and are not the
account name (`relay.CheckNewLogin`).

## 3. Logging in, from each client

| Client | How | What it gets |
|---|---|---|
| Desktop app | First run: 登录 (server, account, password); or 设置新的服务器 with the setup code; "this computer as a machine only" logs in as a machine | A device cert, and its own machine's cert when it runs one |
| Android app | Onboarding: server, account, password | A device cert |
| `aide auth login [server] --account A` | The password from `--password-stdin`, `$AIDE_PASSWORD`, or asked for on the terminal | A grant |
| `aided login [server] --account A` (and `install.sh <server>`, `install.ps1 <server>`) | As the CLI; the install scripts hand `$AIDE_ACCOUNT` / `$AIDE_PASSWORD` to it, the password on its stdin | A machine cert; the background service is installed |
| An MCP app (Claude, ChatGPT…) | The gateway's consent page asks for the account and password (docs/mcp-gateway.md) | A grant whose key the gateway holds (`relay.IssueGrant`), connected at once |
| Her | Her's grant is signed for the owner's device that turns her on (Sign) | — |

A machine logging in takes the name it asks for (its `--name`, or its host
name), unless another of the owner's machines has it: then the server gives
it the next free one (`-2`, `-3`…; `LogInResponse.machine_name`), so agents
can still address `machine/project`. A removed machine's id never comes
back: a machine removed is erased first (`aided erase`), then logs in again under a new key.

## 4. The account and the devices, from any device logged in

Settings › Account (`LocalService.GetLogin`, `SetLogin`):

- **The account's name and password** change with the current password.
  Forgotten, it is set again on the server's own computer:
  `aide-relay password --account A` reads the new one on stdin (then
  restart the server).
- **A new password signs out** every other device and every agent that
  logged in with an earlier password (their device keys and grants go on
  the revocation list), but not the device that changed it, and not the
  machines: they run the owner's tasks, and are removed one by one. The
  answer says how many were signed out.
- **Logging in can be turned off** (and on again, with the password): no new
  device, machine or agent can log in; those logged in stay.

The devices (`ListDevices`, `RemoveDevice`): every device that logged in, and
each one set up before accounts (a root key of its own in the root cert,
`legacy`). Any device signs out another (never itself):

- one that logged in: its device key is revoked (`RevocationList.
  revoked_device_keys`): the relay drops and refuses it, every machine
  refuses its tunnels and closes those it has; its own machine is removed;
- one from before accounts: the server signs the next root cert without
  its root key, signs again (one version up for a grant) every machine cert
  and grant that key had signed, and removes its own machine.

A device signed out learns it when it next connects ("this device was signed
out"), shows it, and leaves: it can log in again.

## 5. From before accounts: converting the old password login

A server set up before accounts has no key of its own. If it has the old
password login (docs/password-login.md: the password key, a root key, sealed
by the password), **the first login with that account and password converts
it** (`relay.convertOld`), at the first LogIn or SetLogin with it:

1. the password opens the old sealed key (Argon2id with the old record's
   costs; the auth key must match its verifier);
2. the server makes its key, and the password key signs the next root cert:
   every root key it listed but itself, and the server's key;
3. every machine cert and grant the password key had signed is signed
   again by the server's key (a grant one version up), and pushed;
4. the account is kept as an Argon2id hash of the same password, and the
   old record is deleted.

Devices, machines and agents approved before keep working: their
credentials were signed by device root keys still listed. A device that had
logged in with the old password signed its device cert with the password
key, which is no longer listed: it is refused, and logs in again with the
same account and password.

A server set up before accounts **without** a password login cannot be
converted (nothing it holds can sign the root cert): turn the password login
on with the app from before this change first, then update; or erase
everything and set the server up again.

## 6. Several owners on one device

A device keeps a profile for each owner it logged in to (docs/trusted-devices.md
§4.8 described them; nothing there changed but how a profile is made). Logging
in to an owner another profile already has is refused: switch to it.

## 7. What it costs (the owner's choice, stated plainly)

| Attack | Result |
|---|---|
| Someone who knows the password | Is the owner: logs in any device, machine or agent. Change the password (its devices and agents go), or turn logging in off |
| A stolen copy of the server's data | **Holds the server's key: can sign itself in as anything**, reach every machine as an agent or as the owner, read sessions through them. The account's hash can also be guessed offline at one Argon2id (64 MiB) per try |
| Whoever runs the server, or takes it over | The same: the server is the authority. It also sees the password at every login (over its TLS) |
| A server without HTTPS (plain HTTP, the relay warns) | The password crosses the network readable |
| Guessing online | 20 login calls a minute per address; 5 wrong passwords in a row lock the account for a minute, doubling up to an hour (a stranger can lock the owner out that long) |
| A device that logged in, stolen | Sign it out from another device, or change the password |
| Something on the path without the server's key (the network, a relay that lost its key) | Can't log in without the password, can't forge credentials, can't read tunnels (e2e/evilrelay_test.go) |

There is no other approval left to stop any of these: no device confirms a
new machine, agent or device, and no agent action waits for the owner
(docs/her-server.md: Her runs without asking).

## 8. Code

| Area | What |
|---|---|
| `proto/aide/relay/v1` | `SetUp`, `LogIn` (public); `Sign`, `GetLogin`, `SetLogin`, `ListDevices`, `RemoveDevice` (owner); `Joiner`, `JoinKind`, `Account`, `OldPasswordLogin` |
| `proto/aide/identity/v1` | `RevocationList.revoked_device_keys`, `EraseOrder.by` |
| `proto/aide/local/v1` | `Setup`, `LogIn`, `GetLogin`, `SetLogin` |
| `pkg/relay/account.go` | The account, the server's key, logging in, signing for the owner, devices, the conversion, `SetPassword` |
| `internal/machine/login.go` | `Machine.LogIn`, `LogInTo`, `CheckLogIn` |
| `internal/owner/login.go` | A device logging in or setting up; `signRoot` through the server |
| `internal/cli/login.go` | `aide auth login` |
| `cmd/aided` | `aided login` |
| `cmd/aide-relay` | `setup-code`, `password` |
| `pkg/mcpgw` | The consent page logs in (`relay.IssueGrant`) |
| Tests | `pkg/relay/account_test.go` (`TestAccountLogIn`, `TestAccountSignsAndSignsOut`, `TestAccountFromAnOldPasswordLogin`); e2e `TestUserJourney`, `TestMachineOnlyMac`, `TestAccountsOnOneDevice`, `TestMCPGateway`, `TestMCPStrangers`, `TestEraseEverything`, `TestAndroidCore`, `TestEvilRelay` |

<!-- CI 样本（工单 #33），不合并 -->
