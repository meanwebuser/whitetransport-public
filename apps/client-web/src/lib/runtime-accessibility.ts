/** Returns the native accessibility action exposed by the runtime power toggle. */
export function whiteTransportToggleLabel(isOnline: boolean): string {
  return isOnline ? 'Disconnect WhiteTransport' : 'Connect WhiteTransport';
}
