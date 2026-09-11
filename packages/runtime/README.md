# Memoh Remote Runtime CLI

The CLI saves a connection configuration and lets the current user's OS service
manager run it in the background. Install Node and the CLI with your package
manager in a persistent location:

```sh
npm install -g @memohai/runtime
memoh-runtime enroll --server https://memoh.example --key "$MEMOH_RUNTIME_KEY"
memoh-runtime service install
memoh-runtime service start
```

`enroll` saves the server URL, key, and optional `--team-id` in
`~/.memoh/runtime.json`. The file is written atomically with private permissions.
Changing an existing connection or repairing a malformed file requires
`--replace`. Restart a running service to apply the new configuration.

并发执行 `enroll` 时，CLI 使用 `~/.memoh/runtime.json.lock/` 串行完成
读取、比较与写入，最多等待 30 秒。正常结束或报错都会释放锁；若进程被强制
终止而留下锁目录，请检查其中 `owner.json` 的 PID，确认原进程已经退出后
再删除锁目录并重试。不要删除仍有进程持有的锁。

macOS 上已有配置和目录的直接、继承 ACL 都会检查；向其他账户开放凭证读取
或目录写入权限时会拒绝操作，CLI 不会自动修改这些已有权限。

`run` reads the saved configuration and runs in the foreground. Connection flags
or environment variables make a temporary connection without saving it.
`--config <file>` reads that file instead and ignores connection environment
variables. The original invocation without the `run` subcommand still works.

```sh
memoh-runtime run
memoh-runtime service status
memoh-runtime service restart
memoh-runtime service stop
memoh-runtime service uninstall
```

`service install` registers a stopped service pointing at the existing CLI and
the Node entry found in `PATH`, preserving Node symlinks. It does not save
credentials. Reinstalling stops the old process; use `service start` afterward.
Keep that CLI installation available; an `npx` cache is not a persistent install.
Package managers own program installation and upgrades. If an upgrade moves Node
or the CLI, rerun `service install` and `service start`.

The OS owns process supervision and automatic startup:

| OS | Service manager | Automatic startup | Logs |
| --- | --- | --- | --- |
| macOS | launchd user agent | User login | `~/.memoh/runtime/logs/` |
| Linux | systemd user service | User manager startup | `journalctl --user -u memoh-runtime` |
| Windows | Task Scheduler, task scoped to the user's SID | User login; requires an interactive session | `~/.memoh/runtime/logs/runtime.log` |

On Linux, `loginctl enable-linger` lets the user manager run after logout and at
boot. `stop` leaves automatic startup registered. `uninstall` removes the native
service definition and retains the saved configuration and logs.

`status` (or `status --json`) reports the native service state, not connectivity
to the server. `start` and `restart` check the saved configuration first; `stop`
and `uninstall` also work when that configuration is missing or malformed.
