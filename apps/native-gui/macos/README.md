# macOS Network Extension integration

Этот каталог содержит macOS-часть настоящего Wails desktop client: общий `WhiteTransportMacOS.framework`, Packet Tunnel Provider `.appex`, Darwin C-archive packet engine и тесты. Отдельного placeholder `NSApplication` target здесь нет. Framework и extension встраиваются в bundle, собранный из `apps/native-gui`.

Актуальный статус компонентов хранится только в корневом `PROJECT_STATUS.md`.

## Targets и границы

- `WhiteTransportCore` собирает `WhiteTransportMacOS.framework`: wire-контракты, строгий runtime profile, App Group store, `VPNManager` и C ABI для Wails.
- `WhiteTransportPacketTunnel` собирает `WhiteTransportPacketTunnel.appex` с principal class `WhiteTransportPacketTunnel.PacketTunnelProvider` и bundle ID `com.meanwebuser.whitetransport.packet-tunnel`.
- `WhiteTransportCoreTests` проверяет те же contract, lifecycle, FD ownership, profile и App Group границы через Xcode.
- `EngineBridge` импортирует `core/go/mobile` и экспортирует `WTStartTun2Socks`, `WTStopTun2Socks`, `WTLastError` и `WTFreeCString`. Universal static archive линкуется только в `.appex`.

Wails app и extension используют App Group `group.com.meanwebuser.whitetransport`. Реальная установка требует подписанного containing app, provisioning profile и разрешённых Apple Network Extension/App Group entitlements. Unsigned/source-only bundle доказывает сборочную топологию, но не установку или пользовательское разрешение.

Перед подписанной сборкой на Mac можно выполнить безопасный read-only preflight:

```bash
apps/native-gui/macos/scripts/macos-signing-preflight.sh
```

Скрипт только читает identities из login keychain и локальные provisioning profiles. Он показывает количество profiles и проверяет `packet-tunnel-provider` вместе с App Group `group.com.meanwebuser.whitetransport`; при отсутствии совместимого profile завершается с actionable ошибкой. Он не запускает Xcode, provisioning/account updates или сборку. Для диагностики другого каталога profiles используйте `WT_MACOS_PROFILE_DIR=/path/to/profiles`.

## Runtime profile и connected proof

Go daemon передаёт в системный VPN только свежий подтверждённый профиль со следующими обязательными полями:

- `daemon_instance_id`;
- монотонный `profile_revision`;
- 64-символьный SHA-256 `profile_hash`;
- `session_id`;
- credential-free loopback `socks_endpoint`;
- authoritative carrier/control endpoints и точный DNS snapshot для каждого endpoint host.

Оба route mode отклоняются без доказанных host exclusions. Для `full_tunnel` используются IPv4/IPv6 default routes, для `destination_split` — только заданные destination CIDR; в обоих случаях carrier/control адреса исключаются точными `/32` и `/128` routes. Дополнительные подтверждённые Go bridge маршруты `user_bypass_cidrs` добавляются к excluded routes; malformed CIDR и любой prefix `0` отклоняются, чтобы пользовательский bypass не мог выключить системный туннель целиком.

`WTSystemVPNStart` ограниченно ждёт одновременно два сигнала: macOS сообщает `NEVPNStatus.connected`, а provider записывает `connected` для точно того же daemon/profile/session identity. Один только `NEVPNStatus.connected`, старый App Group status или совпавший label не считается успехом. C ABI response возвращает identity, `provider_state`, `provider_status_matched` и последний структурированный status, поэтому Go может продолжить bounded polling без подмены профиля.

## App Group exchange

Extension пишет `status-v2.json` и bounded rotating JSONL `events-v2.jsonl`/`events-v2.previous.jsonl` под межпроцессным `store-v2.lock`. Status содержит `provider_state`, `daemon_instance_id`, `profile_revision`, `profile_hash` и `session_id`; sequence восстанавливается с диска и остаётся монотонным после restart. Файлы имеют mode `0600`, encoders/decoders создаются на каждый вызов, callback доставляется на queue вызывающей стороны, credential и endpoint metadata редактируются рекурсивно до записи.

Этот канал не переносит TokenStore или carrier credentials.

## Порядок сборки на Mac

Framework и extension должны быть собраны до Wails binary, потому что Darwin cgo wrapper линкуется с `WhiteTransportMacOS.framework`, а postprocess затем встраивает framework и `.appex` в уже созданный Wails bundle.

```bash
cd apps/native-gui/macos

swift test --disable-sandbox -Xswiftc -swift-version -Xswiftc 6

xcodebuild -project WhiteTransport.xcodeproj \
  -target WhiteTransportCore -configuration Release \
  CODE_SIGNING_ALLOWED=NO SYMROOT=/tmp/wt-macos-products build

xcodebuild -project WhiteTransport.xcodeproj \
  -target WhiteTransportPacketTunnel -configuration Release \
  CODE_SIGNING_ALLOWED=NO SYMROOT=/tmp/wt-macos-products build

xcodebuild -project WhiteTransport.xcodeproj \
  -scheme WhiteTransport -destination 'platform=macOS' \
  CODE_SIGNING_ALLOWED=NO test
```

После появления Darwin Wails wrapper сборка binary должна получить framework search path на предыдущий products directory, затем запускается source-only postprocess:

```bash
cd apps/native-gui
CGO_ENABLED=1 \
CGO_CFLAGS='-F/tmp/wt-macos-products/Release' \
CGO_LDFLAGS='-F/tmp/wt-macos-products/Release -framework WhiteTransportMacOS -Wl,-rpath,@executable_path/../Frameworks' \
wails build -platform darwin/universal

macos/scripts/package-wails-network-extension.sh --source-only \
  build/bin/WhiteTransport.app /tmp/wt-macos-products/Release
```

Тот же порядок автоматизирован без secret packaging:

```bash
apps/native-gui/macos/scripts/build-source-only-wails-network-extension.sh /tmp/wt-macos-products
```

Для локального credentialed-пакета оператор обязан передать каждый уже
проверенный файл явным абсолютным путём; скрипт ничего не ищет в `artifacts/`
и не печатает содержимое TokenStore:

```bash
apps/native-gui/macos/scripts/package-wails-network-extension.sh \
  --credentialed \
  "$PWD/build/bin/WhiteTransport.app" \
  /tmp/wt-macos-products/Release \
  /absolute/path/whitetransportd \
  /absolute/path/token-store.json \
  /absolute/path/sing-box
```

Credentialed mode copies the daemon and `sing-box` with mode `0755`, and the
TokenStore with mode `0600`, then runs the same bundle topology verifier. It is
not a signing or provisioning step: a Network Extension install still needs a
signed app, matching provisioning profile, and approved entitlements.

`package-wails-network-extension.sh --source-only` удаляет случайно попавший plaintext TokenStore, копирует framework и `.appex`, добавляет MIT notice для pinned `xjasonlyu/tun2socks`, затем запускает topology verifier. Verifier требует, чтобы Wails executable действительно импортировал `WhiteTransportMacOS.framework`, а framework экспортировал все шесть `WTSystemVPN*` symbols. Production TokenStore сейчас содержит смешанные node/client roles, поэтому такой bundle нельзя объявлять готовым full-secret приложением до появления отдельного безопасного client principal.

## Проверки packet engine и bundle topology

```bash
WT_TEST_ROUNDS=3 EngineBridge/DataPlane/test-c-abi-data-plane.sh
scripts/test-package-wails-network-extension.sh
scripts/verify-wails-bundle.sh /path/to/WhiteTransport.app
```

`test-c-abi-data-plane.sh` — отдельный C-ABI engine smoke lane: на каждом fresh-FD цикле он доказывает IPv4/IPv6 TCP и UDP, успешный `WTStopTun2Socks` и `EBADF` после передачи ownership engine. Это не PacketTunnel/Network Extension install и не installed-client proof. Topology verifier требует Wails executable, framework в `Contents/Frameworks`, extension в `Contents/PlugIns`, MIT notice в `Contents/Resources/third-party-notices`; engine symbols разрешены только в `.appex`.

Wails framework экспортирует `WTSystemVPNPermission`, `WTSystemVPNStart`, `WTSystemVPNStop`, `WTSystemVPNStatus`, `WTSystemVPNLogs` и `WTSystemVPNFreeCString`. Go wrapper обязан освобождать каждый returned C string через `WTSystemVPNFreeCString`.

Нормальный `WTSystemVPNStop` дожидается завершения bounded provider cleanup и системного stop, затем синхронно возвращает `state: disconnected`, `provider_state: disconnected` и точную identity остановленного runtime-профиля. После формирования ответа host очищает active identity, чтобы последующий status не выдавал остановленный профиль за активный.

Каждый Go-профиль обязан передать `profile_valid_until`: минимальный срок из profile expiry и всех DNS snapshot expiry, нормализованный в UTC до целой секунды. Extension валидирует этот deadline без clock-skew fallback, публикует его в App Group/Wails status и привязывает к generation-bound таймеру. По истечении срока provider останавливает bridge, один раз очищает Network Extension settings и только затем отменяет tunnel с ошибкой. Lease определяется как identity плюс `profile_valid_until`; даже renewal только deadline требует подтверждённого `Stop → Start`, live update не поддерживается.

Успешный Wails Stop завершается только после фактического `NEVPNStatus.disconnected` (либо уже наблюдаемого backend `systemState == disconnected`). Если системное событие не приходит в bounded timeout, C ABI возвращает явную ошибку и не очищает active lease, поэтому новый профиль не стартует поверх незавершённого teardown.

## Авторизация direct-helper

Основной direct backend запускает bundled `direct-helper` от обычного пользователя через системный macOS administrator prompt (`osascript` + `do shell script ... with administrator privileges`). GUI не читает, не хранит и не передаёт пароль; аргументы helper передаются в фиксированный AppleScript и экранируются через `quoted form`.

Автоматизированный тест на установленном Mac может явно сохранить прежний fail-fast путь после отдельного `sudo -v`:

```bash
WT_DIRECT_HELPER_AUTH_MODE=sudo-noninteractive /path/to/WhiteTransport.app/Contents/MacOS/WhiteTransport
```

Этот override предназначен только для test harness. Без него direct backend всегда показывает native authorization prompt и не зависит от заранее прогретого sudo timestamp. SMAppService probe остаётся отдельной проверкой XPC helper и не требуется для primary direct backend.
