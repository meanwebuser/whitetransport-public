import unittest
from pathlib import Path

from android_auto_debug_ui import (
    connect_center,
    has_connect_control,
    is_connected,
    is_disconnected,
)


DISCONNECTED_XML = '''
<node class="android.widget.ToggleButton" text="Подключиться"
      content-desc="" clickable="true" bounds="[231,246][488,503]" />
'''

CONNECTED_XML = '''
<node class="android.widget.ToggleButton" text="Отключиться"
      content-desc="" clickable="true" bounds="[231,246][488,503]" />
<node class="android.view.View" text="Подключено" content-desc="" />
'''

CONNECTED_HOME_XML = '''
<node class="android.widget.ToggleButton" text="Отключить"
      content-desc="" clickable="true" bounds="[231,246][488,503]" />
<node class="android.view.View" text="Подключено" content-desc="" />
'''


class AndroidAutoDebugUiTests(unittest.TestCase):
    def test_localized_connect_label_is_actionable(self) -> None:
        self.assertEqual(connect_center(DISCONNECTED_XML), (359, 374))
        self.assertTrue(has_connect_control(DISCONNECTED_XML))
        self.assertTrue(is_disconnected(DISCONNECTED_XML))

    def test_localized_connected_label_is_actionable(self) -> None:
        self.assertTrue(is_connected(CONNECTED_XML))

    def test_home_screen_disconnect_label_is_actionable(self) -> None:
        self.assertTrue(is_connected(CONNECTED_HOME_XML))

    def test_runner_bounds_ui_dump_and_stops_consent_poll_on_native_failure(self) -> None:
        source = (Path(__file__).with_name('run_android_auto_debug.sh')).read_text()

        self.assertIn('UI_DUMP_TIMEOUT_SECONDS', source)
        self.assertIn("WT_RUNTIME_UI failed backend=", source)


if __name__ == "__main__":
    unittest.main()
