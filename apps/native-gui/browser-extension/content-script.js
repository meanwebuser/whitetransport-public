/* global chrome */

"use strict";

void chrome.runtime.sendMessage({ type: "wt-browser-page-ready" });
