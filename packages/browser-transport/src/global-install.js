import { installWispOverWb } from './install-wisp-over-wb.js';
import {
  createDataChannelByteDuplex,
  createWhitelistBypassByteDuplexConnector,
} from './whitelist-bypass-byte-duplex.js';
import { createWbStreamLiveKitDataChannel, createWbStreamDataChannelFromToken } from './wbstream-livekit-joiner.js';

window.WhiteTransport = {
  installWispOverWb,
  createDataChannelByteDuplex,
  createWhitelistBypassByteDuplexConnector,
  createWbStreamLiveKitDataChannel,
  createWbStreamDataChannelFromToken,
};
