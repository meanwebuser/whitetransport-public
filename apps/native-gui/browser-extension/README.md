# Расширение WhiteTransport для Chrome и Edge

Расширение передаёт выбранную браузерную сессию Телемоста, DION или VK в
локальный CLI WhiteTransport и выполняет его команды обновления. Кнопка
копирования ссылки Телемоста доступна сразу после установки.

## Установка и привязка

1. Скачайте и распакуйте `WhiteTransport-Chrome-0.1.6.zip`. По адресу
   `chrome://extensions` или `edge://extensions` включите режим разработчика
   и загрузите папку `WhiteTransport`.
2. Скачайте CLI той же версии для вашей платформы. На Ubuntu x64, например:

   ```sh
   cd "$HOME/Downloads"
   chmod +x wt-browser-credential-bridge-linux-x64
   ./wt-browser-credential-bridge-linux-x64 --keep-alive
   ```

   На Mac выберите файл с суффиксом `macos-arm64` для Apple Silicon или
   `macos-x64` для Intel и запустите его так же.
3. Вставьте строку `READY` из терминала в расширение и нажмите «Подключить CLI».
   Разрешите доступ к локальной сети, если браузер его запросит. Адрес и ключ
   создаются заново при запуске CLI. Храните строку привязки локально.
4. Откройте страницу сервиса и нажмите «Отправить свежую сессию». Для работы
   по командам включите «Проверять команды CLI каждые 30 секунд».

Сессии передаются по адресу `127.0.0.1`. Ручной экспорт в JSON содержит данные
входа. Используйте его только для локального импорта и удаляйте после применения.

## Команда обновления

CLI читает `browser-refresh.command` в каталоге `WT_NATIVE_GUI_CONFIG_DIR`.
Каталог по умолчанию — `~/.config/WhiteTransport`. Например, чтобы запросить
сессию DION:

```sh
dir="$HOME/.config/WhiteTransport"
mkdir -p "$dir"
(
  umask 077
  command_file="$(mktemp "$dir/.browser-refresh.XXXXXX")"
  printf '%s\n' '{"command":"update","url":"https://dion.vc/"}' >"$command_file"
  mv "$command_file" "$dir/browser-refresh.command"
)
```

CLI забирает команду один раз. Для успешного импорта браузерный профиль должен
быть авторизован в выбранном сервисе. После проверки можно отключить polling
или всю привязку в расширении.

## Проверка из исходников

Запустите `make test-browser-extension-bridge`. Для подготовки ZIP, CLI и
SHA‑256 используйте `make package-browser-extension`.
