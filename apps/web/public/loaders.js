"use strict";

(function () {
	const registry = new Map();
	const DEFAULT_LOADER = "cat-cable";
	const STORAGE_KEY = "mobile-browser:loader";

	function el(tag, className = "", text = "") {
		const node = document.createElement(tag);
		if (className) node.className = className;
		if (text) node.textContent = text;
		return node;
	}

	function register(name, factory) {
		registry.set(name, factory);
	}

	function getSelectedName() {
		try {
			return localStorage.getItem(STORAGE_KEY) || DEFAULT_LOADER;
		} catch {
			return DEFAULT_LOADER;
		}
	}

	function select(name) {
		if (!registry.has(name)) throw new Error(`Unknown loader: ${name}`);
		localStorage.setItem(STORAGE_KEY, name);
	}

	function create(overlay, name = getSelectedName()) {
		const factory = registry.get(name) || registry.get(DEFAULT_LOADER);
		if (!factory) throw new Error("No loaders registered");
		return factory(overlay);
	}

	register("cat-cable", (overlay) => {
		let loopTimer = null;
		let anim = null;

		function build() {
			overlay.textContent = "";
			overlay.dataset.loader = "cat-cable";
			const scene = el("div", "loader-scene cat-cable-scene");
			const cable = el("div", "cat-cable");
			const runner = el("div", "cat-runner");
			runner.innerHTML = `
				<div class="cat-body">
					<div class="cat-ear left"></div>
					<div class="cat-ear right"></div>
					<div class="cat-face">
						<div class="cat-eye left"></div>
						<div class="cat-eye right"></div>
						<div class="cat-nose"></div>
					</div>
					<div class="cat-tail"></div>
				</div>`;
			const caption = el("div", "loader-caption");
			caption.append(el("div", "loader-title", "Котик тянет свободу за кабель…"));
			caption.append(el("div", "loader-subtitle", "Если страница открылась — бытие снова победило реестр."));
			scene.append(cable, runner, caption);
			overlay.append(scene);
			return { scene, cable, runner };
		}

		const dom = build();

		function randomLane() {
			const sceneHeight = dom.scene.clientHeight || window.innerHeight || 800;
			const topMin = Math.max(128, sceneHeight * 0.34);
			const topMax = Math.max(topMin + 24, sceneHeight * 0.78);
			return Math.floor(topMin + Math.random() * (topMax - topMin));
		}

		function runAcrossScreen() {
			if (overlay.hidden) return;
			const lane = randomLane();
			const sceneWidth = dom.scene.clientWidth || window.innerWidth || 1200;
			const startX = -150;
			const endX = sceneWidth + 150;
			const duration = 4200 + Math.floor(Math.random() * 1800);
			dom.runner.style.top = `${lane - 24}px`;
			dom.cable.style.top = `${lane + 16}px`;
			if (anim) {
				try { anim.cancel(); } catch {}
			}
			anim = dom.runner.animate(
				[{ transform: `translateX(${startX}px)` }, { transform: `translateX(${endX}px)` }],
				{ duration, iterations: 1, easing: "linear", fill: "forwards" }
			);
			anim.onfinish = () => {
				if (overlay.hidden) return;
				loopTimer = setTimeout(runAcrossScreen, 120 + Math.random() * 320);
			};
		}

		function stopLoop() {
			if (loopTimer) clearTimeout(loopTimer);
			loopTimer = null;
			if (anim) {
				try { anim.cancel(); } catch {}
				anim = null;
			}
		}

		return {
			name: "cat-cable",
			start({ text } = {}) {
				const title = overlay.querySelector(".loader-title");
				if (title && text) title.textContent = text;
				stopLoop();
				runAcrossScreen();
			},
			stop() {
				stopLoop();
			},
			destroy() {
				stopLoop();
				overlay.textContent = "";
			},
		};
	});

	window.MobileBrowserLoaders = {
		register,
		create,
		select,
		list: () => [...registry.keys()],
		getSelectedName,
	};
})();
