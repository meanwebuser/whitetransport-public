"use strict";
const SW_VERSION = "202606021704";
const stockSW = `./sw.js?v=${SW_VERSION}`;

/**
 * List of hostnames that are allowed to run serviceworkers on http://
 */
const swAllowedHostnames = ["localhost", "127.0.0.1"];

/**
 * Global util
 * Used in 404.html and index.html
 */
async function registerSW() {
	if (!navigator.serviceWorker) {
		if (
			location.protocol !== "https:" &&
			!swAllowedHostnames.includes(location.hostname)
		)
			throw new Error("Service workers cannot be registered without https.");

		throw new Error("Your browser doesn't support service workers.");
	}

	const registration = await navigator.serviceWorker.register(stockSW, { updateViaCache: "none" });
	await registration.update().catch(() => {});
	if (registration.waiting) registration.waiting.postMessage({ type: "SKIP_WAITING" });
	if (navigator.serviceWorker.controller && !navigator.serviceWorker.controller.scriptURL.includes(`v=${SW_VERSION}`)) {
		await new Promise((resolve) => {
			const timer = setTimeout(resolve, 3000);
			const onChange = () => {
				clearTimeout(timer);
				navigator.serviceWorker.removeEventListener("controllerchange", onChange);
				resolve();
			};
			navigator.serviceWorker.addEventListener("controllerchange", onChange, { once: true });
		});
	}
	return registration;
}
