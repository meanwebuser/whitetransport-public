/*global Ultraviolet*/
self.__uv$config = {
    prefix: '/uv/service/',
    encodeUrl: Ultraviolet.codec.xor.encode,
    decodeUrl: Ultraviolet.codec.xor.decode,
    handler: '/uv/uv.handler.js?v=20260531_sharedworker1',
    client: '/uv/uv.client.js',
    bundle: '/uv/uv.bundle.patched.js?v=20260531_sharedworker1',
    config: '/uv/uv.config.js?v=20260531_sharedworker1',
    sw: '/uv/uv.sw.js?v=20260531_sharedworker1',
};
