export namespace main {
	
	export class launchConnectResult {
	    mode: string;
	    proofBoundary?: string;
	    targetNodeId: string;
	    activeNodeId?: string;
	    transportState?: string;
	    systemVpnState?: string;
	    externalIp?: string;
	    systemRouteProbeRequested?: boolean;
	    systemRouteProbePassed?: boolean;
	    systemRouteProbeTarget?: string;
	    systemRouteProbeResponse?: string;
	    systemRouteIp?: string;
	    systemRouteProbeMarker?: string;
	    systemRouteProbeError?: string;
	    bypassRouteProbeRequested?: boolean;
	    bypassRouteProbePassed?: boolean;
	    bypassRouteProbeTarget?: string;
	    bypassRouteProbeResponse?: string;
	    bypassRouteIp?: string;
	    bypassRouteProbeMarker?: string;
	    bypassRouteProbeError?: string;
	    passed: boolean;
	    error?: string;
	    completedAt: string;
	    logPath?: string;
	    resultPath?: string;
	
	    static createFrom(source: any = {}) {
	        return new launchConnectResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.proofBoundary = source["proofBoundary"];
	        this.targetNodeId = source["targetNodeId"];
	        this.activeNodeId = source["activeNodeId"];
	        this.transportState = source["transportState"];
	        this.systemVpnState = source["systemVpnState"];
	        this.externalIp = source["externalIp"];
	        this.systemRouteProbeRequested = source["systemRouteProbeRequested"];
	        this.systemRouteProbePassed = source["systemRouteProbePassed"];
	        this.systemRouteProbeTarget = source["systemRouteProbeTarget"];
	        this.systemRouteProbeResponse = source["systemRouteProbeResponse"];
	        this.systemRouteIp = source["systemRouteIp"];
	        this.systemRouteProbeMarker = source["systemRouteProbeMarker"];
	        this.systemRouteProbeError = source["systemRouteProbeError"];
	        this.bypassRouteProbeRequested = source["bypassRouteProbeRequested"];
	        this.bypassRouteProbePassed = source["bypassRouteProbePassed"];
	        this.bypassRouteProbeTarget = source["bypassRouteProbeTarget"];
	        this.bypassRouteProbeResponse = source["bypassRouteProbeResponse"];
	        this.bypassRouteIp = source["bypassRouteIp"];
	        this.bypassRouteProbeMarker = source["bypassRouteProbeMarker"];
	        this.bypassRouteProbeError = source["bypassRouteProbeError"];
	        this.passed = source["passed"];
	        this.error = source["error"];
	        this.completedAt = source["completedAt"];
	        this.logPath = source["logPath"];
	        this.resultPath = source["resultPath"];
	    }
	}
	export class macOSAuthorizationProbeResult {
	    supported: boolean;
	    registered: boolean;
	    authorized: boolean;
	    operation: string;
	    helperVersion: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new macOSAuthorizationProbeResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.supported = source["supported"];
	        this.registered = source["registered"];
	        this.authorized = source["authorized"];
	        this.operation = source["operation"];
	        this.helperVersion = source["helperVersion"];
	        this.error = source["error"];
	    }
	}
	export class nativeCapabilities {
	    host: string;
	    transport: boolean;
	    endpoints: boolean;
	    logs: boolean;
	    splitRouting: boolean;
	    proxyRouting: boolean;
	    systemVpn: boolean;
	    requestVpnPermission: boolean;
	    startSystemVpn: boolean;
	    stopSystemVpn: boolean;
	    smokeTest: boolean;
	
	    static createFrom(source: any = {}) {
	        return new nativeCapabilities(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.host = source["host"];
	        this.transport = source["transport"];
	        this.endpoints = source["endpoints"];
	        this.logs = source["logs"];
	        this.splitRouting = source["splitRouting"];
	        this.proxyRouting = source["proxyRouting"];
	        this.systemVpn = source["systemVpn"];
	        this.requestVpnPermission = source["requestVpnPermission"];
	        this.startSystemVpn = source["startSystemVpn"];
	        this.stopSystemVpn = source["stopSystemVpn"];
	        this.smokeTest = source["smokeTest"];
	    }
	}
	export class routingSettings {
	    mode: string;
	    lan_access: boolean;
	
	    static createFrom(source: any = {}) {
	        return new routingSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.lan_access = source["lan_access"];
	    }
	}
	export class splitRoutingSettings {
	    mode: string;
	    lan_access: boolean;
	    destinations?: string[];
	
	    static createFrom(source: any = {}) {
	        return new splitRoutingSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.lan_access = source["lan_access"];
	        this.destinations = source["destinations"];
	    }
	}

}

export namespace runtime {
	
	export class BuildSummary {
	    version: string;
	    commit: string;
	    date: string;
	
	    static createFrom(source: any = {}) {
	        return new BuildSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.commit = source["commit"];
	        this.date = source["date"];
	    }
	}
	export class ClientCredentialSummary {
	    id: string;
	    platform: string;
	    label?: string;
	    has_token: boolean;
	    has_cookie: boolean;
	    // Go type: time
	    created_at: any;
	
	    static createFrom(source: any = {}) {
	        return new ClientCredentialSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.platform = source["platform"];
	        this.label = source["label"];
	        this.has_token = source["has_token"];
	        this.has_cookie = source["has_cookie"];
	        this.created_at = this.convertValues(source["created_at"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class DaemonSupervisorStatus {
	    state: string;
	    message: string;
	    pid?: number;
	    api_url?: string;
	    binary_path?: string;
	    config_path?: string;
	    started_at?: string;
	    last_health_at?: string;
	
	    static createFrom(source: any = {}) {
	        return new DaemonSupervisorStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.message = source["message"];
	        this.pid = source["pid"];
	        this.api_url = source["api_url"];
	        this.binary_path = source["binary_path"];
	        this.config_path = source["config_path"];
	        this.started_at = source["started_at"];
	        this.last_health_at = source["last_health_at"];
	    }
	}
	export class DesktopStatus {
	    state: string;
	    connected: boolean;
	    transport_state: string;
	    system_vpn_state: string;
	    message: string;
	    runtime_state: string;
	    active_node_id?: string;
	    session_id?: string;
	    socks_listen?: string;
	    discovered_servers: number;
	    available_servers: number;
	    healthy_carriers: number;
	    unhealthy_carriers: number;
	    reconnect_attempts?: number;
	    last_runtime_error?: string;
	    runtime_build: BuildSummary;
	    diagnostics_available: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DesktopStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.connected = source["connected"];
	        this.transport_state = source["transport_state"];
	        this.system_vpn_state = source["system_vpn_state"];
	        this.message = source["message"];
	        this.runtime_state = source["runtime_state"];
	        this.active_node_id = source["active_node_id"];
	        this.session_id = source["session_id"];
	        this.socks_listen = source["socks_listen"];
	        this.discovered_servers = source["discovered_servers"];
	        this.available_servers = source["available_servers"];
	        this.healthy_carriers = source["healthy_carriers"];
	        this.unhealthy_carriers = source["unhealthy_carriers"];
	        this.reconnect_attempts = source["reconnect_attempts"];
	        this.last_runtime_error = source["last_runtime_error"];
	        this.runtime_build = this.convertValues(source["runtime_build"], BuildSummary);
	        this.diagnostics_available = source["diagnostics_available"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class DesktopTelemetry {
	    external_ip?: string;
	    latency_ms?: number;
	    active_node_id?: string;
	    measured_at?: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new DesktopTelemetry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.external_ip = source["external_ip"];
	        this.latency_ms = source["latency_ms"];
	        this.active_node_id = source["active_node_id"];
	        this.measured_at = source["measured_at"];
	        this.error = source["error"];
	    }
	}
	export class DiagnosticStep {
	    name: string;
	    status: string;
	    detail?: string;
	    error?: string;
	    started_at: string;
	    ended_at: string;
	
	    static createFrom(source: any = {}) {
	        return new DiagnosticStep(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.status = source["status"];
	        this.detail = source["detail"];
	        this.error = source["error"];
	        this.started_at = source["started_at"];
	        this.ended_at = source["ended_at"];
	    }
	}
	export class DiagnosticResult {
	    passed: boolean;
	    steps: DiagnosticStep[];
	    status: DesktopStatus;
	
	    static createFrom(source: any = {}) {
	        return new DiagnosticResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.passed = source["passed"];
	        this.steps = this.convertValues(source["steps"], DiagnosticStep);
	        this.status = this.convertValues(source["status"], DesktopStatus);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class LogLine {
	    timestamp: string;
	    level: string;
	    message: string;
	    fields?: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new LogLine(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.timestamp = source["timestamp"];
	        this.level = source["level"];
	        this.message = source["message"];
	        this.fields = source["fields"];
	    }
	}
	export class RuntimeResourceCandidate {
	    kind: string;
	    source: string;
	    target: string;
	    exists: boolean;
	    executable: boolean;
	    required: boolean;
	    status: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new RuntimeResourceCandidate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.source = source["source"];
	        this.target = source["target"];
	        this.exists = source["exists"];
	        this.executable = source["executable"];
	        this.required = source["required"];
	        this.status = source["status"];
	        this.error = source["error"];
	    }
	}
	export class RuntimeResourceSummary {
	    mode: string;
	    runtime_api_url: string;
	    supervisor_state: string;
	    working_directory?: string;
	    executable_path?: string;
	    repo_root?: string;
	    candidates: RuntimeResourceCandidate[];
	    missing_required?: string[];
	    diagnostics_notice?: string;
	
	    static createFrom(source: any = {}) {
	        return new RuntimeResourceSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.runtime_api_url = source["runtime_api_url"];
	        this.supervisor_state = source["supervisor_state"];
	        this.working_directory = source["working_directory"];
	        this.executable_path = source["executable_path"];
	        this.repo_root = source["repo_root"];
	        this.candidates = this.convertValues(source["candidates"], RuntimeResourceCandidate);
	        this.missing_required = source["missing_required"];
	        this.diagnostics_notice = source["diagnostics_notice"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ServerSummary {
	    id: string;
	    label: string;
	    country?: string;
	    region?: string;
	    available: boolean;
	    latency_ms?: number;
	    capabilities?: string[];
	    last_seen_at?: string;
	
	    static createFrom(source: any = {}) {
	        return new ServerSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.country = source["country"];
	        this.region = source["region"];
	        this.available = source["available"];
	        this.latency_ms = source["latency_ms"];
	        this.capabilities = source["capabilities"];
	        this.last_seen_at = source["last_seen_at"];
	    }
	}

}

