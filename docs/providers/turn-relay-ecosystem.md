# TURN-relay экосистема (VK / WB / Telemost): карта и выводы для WhiteTransport

Дата: 2026-09-10. Источник: разбор списка «Похожие проекты» из README
[cacggghp/vk-turn-proxy](https://github.com/cacggghp/vk-turn-proxy) (флагман, ~4000★),
статья Lionheart <https://habr.com/ru/articles/1017410/>, live-пробы STUN с RU-vantage.

## Карта экосистемы

| Проект | Что делает | Заметки |
|---|---|---|
| `cacggghp/vk-turn-proxy` | WireGuard/Hysteria через TURN-серверы звонков VK (Telemost-путь в README вычеркнут) | флагман; главная боль — VK-капча (issues #181–183) |
| `Urtyom-Alyanov/turn-proxy` | Rust-реализация того же: TURN как hop-proxy | активная разработка: TCP-over-DTLS (#6), captcha-solver POC (#5) |
| `jaykaiperson/lionheart` | SOCKS5 через TURN **WB Stream**: guest-register → room → join → token → LiveKit WS → перехват TURN-кредов → `pion/turn Allocate()` → KCP+yamux → SOCKS5 | одна команда, без браузера; статья обязательна к прочтению |
| `kulikov0/whitelist-bypass` | проброс через медиасервер SFU ВК и Телемоста | ближайший аналог наших `telemost`/`vkcall` бэкендов |
| `NedgNDG/vk-proxy-auto-installer` | TUI-автоустановщик VK TURN Proxy | |
| Android-клиенты | `samosvalishe/turn-proxy-android` (Material 3, Kotlin), `MYSOREZ/vk-turn-proxy-android`, `kiper2925/wireguard-turn-android` (WG-интеграция), `WINGS-N/WINGSV` (One UI, root-раздaча VPN), `oxsidee/vkpn` (Flutter), `amurcanov/proxy-turn-vk-android` | весь рынок повернут на WireGuard-over-TURN |
| iOS / macOS | `nullcstring/turnbridge`, `denny4-user/vk-turn-proxy-macos-gui` | |

## Ключевые техники, которых у нас нет

1. **TURN-relay carrier (`vkturn`)** — минт TURN-кредов через VK `calls.*` и использование
   TURN-серверов VK как UDP-relay для произвольного трафика (у экосистемы — WireGuard/Hysteria).
   У нас `vkcall` уже парсит `TurnServer`/`StunServer` из join-ответа
   (`whitelist-bypass/relay/vkcall/vkcall.go:123`) — не хватает `pion/turn` Allocate()
   на клиенте и permissions/релея на exit-node.
2. **WB TURN relay mode** — эндпоинт `wb-stream-turn-1.wb.ru:3478`, креды приходят в
   join-флоу WB Stream. Вместо DataChannel — прямой TURN-relay до VPS: штатный WebRTC
   для DPI, скорость заметно выше SCTP-канала (по утверждению статьи).
3. **KCP+yamux как внутренний протокол** — KCP принимает `net.PacketConn`, а relay
   от `pion/turn` реализует этот интерфейс, поэтому KCP не знает про TURN под ним.
   Плюс AES (ключ SHA-256 от пароля) и смарт-ключ `base64(IP|пароль)` для one-command UX.
4. **Captcha-ландшафт** — новые форматы VK-капчи регулярно ломают клиентов экосистемы
   (#182/#183). Наш `captcha_proxy.go` нужно держать готовым к новым форматам;
   кандидат — auto-solver на Playwright (как в #183 у флагмана).

## Live-факты (проверено 2026-09-10, vantage RU / AS25513)

- `stun.sipnet.ru:3478` — отвечает на binding (публичный RU STUN, рабочий).
- `stun.cloudflare.com:3478` — отвечает из RU на момент проверки.
- `stun.vk.com:3478`, `stun.yandex.ru:3478` — молчат: публичных STUN у VK/Яндекса нет,
  TURN/STUN выдаются только per-session внутри signaling звонков.
- `stun.l.google.com:19302` — таймаут из RU (не годится для hole-punching плана хаба).

Вывод для hub P2P: STUN-лист = `stun.sipnet.ru` + `stun.cloudflare.com` + собственный
coturn на VPS; VK/WB TURN использовать только как per-session carrier (см. пп. 1–2).

## Что сделано

- Issue с предложением OpenFlux + WhiteTransport в список «Похожие проекты» флагмана:
  <https://github.com/cacggghp/vk-turn-proxy/issues/186> (от `github-meanwebuser`,
  по прецеденту #184 «shameless self-promotion»).
- `yadocs` carrier (Яндекс Документы, курсор-сообщения в стиле OpenFlux) — уже в работе
  в `whitelist-bypass/relay/yadocs/`.

## Промо-кампания (2026-09-10, от `meanwebuser`, все тикеты уникальны)

Опубликовано 9 issue из 12 целевых репозиториев (черновики — два параллельных
MiniMax-сабагента, валидация: структура/уникальность/формулировки roadmap):

- cacggghp/vk-turn-proxy — [#186](https://github.com/cacggghp/vk-turn-proxy/issues/186)
- Urtyom-Alyanov/turn-proxy — [#10](https://github.com/Urtyom-Alyanov/turn-proxy/issues/10)
- kulikov0/whitelist-bypass — [#180](https://github.com/kulikov0/whitelist-bypass/issues/180)
- nullcstring/turnbridge — [#34](https://github.com/nullcstring/turnbridge/issues/34)
- samosvalishe/turn-proxy-android — [#75](https://github.com/samosvalishe/turn-proxy-android/issues/75)
- MYSOREZ/vk-turn-proxy-android — [#43](https://github.com/MYSOREZ/vk-turn-proxy-android/issues/43)
- WINGS-N/WINGSV — [#80](https://github.com/WINGS-N/WINGSV/issues/80)
- oxsidee/vkpn — [#14](https://github.com/oxsidee/vkpn/issues/14)
- denny4-user/vk-turn-proxy-macos-gui — [#4](https://github.com/denny4-user/vk-turn-proxy-macos-gui/issues/4)

Не покрыто и почему:

- jaykaiperson/lionheart — репозиторий **заархивирован** (read-only, issue создать нельзя).
- NedgNDG/vk-proxy-auto-installer — репозиторий **заархивирован**.
- amurcanov/proxy-turn-vk-android (1166★) — issues отключены мейнтейнером.
- kiper2925/wireguard-turn-android — репозиторий удалён (404).

Финальная ревизия (вечер 2026-09-10): все 9 issue переписаны без нейрослопа
(нет длинных тире, списков, штампов), в каждом обещание кредов в ридме каждому,
чьё подключаем адаптером, MIT и финал «Просто оставьте нам интернет».
Живой лид: WINGS-N в #80 готов взять WhiteTransport как библиотеку
(JNI/AAR по типу libXray, unix socket с токеном, или gRPC), ответ дан,
следующий шаг: собрать gomobile AAR с узким API.

## Кандидаты в roadmap

- `vkturn` TURN-relay carrier (техника 1) — быстро, т.к. креды уже парсим.
- WB TURN relay mode как альтернатива `wbstream` DataChannel (техника 2).
- KCP-транспорт внутри TURN-relay (техника 3) как опция к существующему mux.
