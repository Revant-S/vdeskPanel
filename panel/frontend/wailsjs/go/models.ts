export namespace api {
	
	export class OpView {
	    kind: string;
	    path?: string;
	    from?: string;
	    to?: string;
	
	    static createFrom(source: any = {}) {
	        return new OpView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.path = source["path"];
	        this.from = source["from"];
	        this.to = source["to"];
	    }
	}
	export class CommitView {
	    id: string;
	    device: string;
	    deviceName: string;
	    clock: string;
	    // Go type: time
	    time: any;
	    parents: string[];
	    summary: string;
	    ops: OpView[];
	
	    static createFrom(source: any = {}) {
	        return new CommitView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.device = source["device"];
	        this.deviceName = source["deviceName"];
	        this.clock = source["clock"];
	        this.time = this.convertValues(source["time"], null);
	        this.parents = source["parents"];
	        this.summary = source["summary"];
	        this.ops = this.convertValues(source["ops"], OpView);
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
	export class DeviceView {
	    id: string;
	    name: string;
	    // Go type: time
	    firstSeen: any;
	    retired: boolean;
	    this: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DeviceView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.firstSeen = this.convertValues(source["firstSeen"], null);
	        this.retired = source["retired"];
	        this.this = source["this"];
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
	export class EmptyTrashResult {
	    items: number;
	    bytes: number;
	
	    static createFrom(source: any = {}) {
	        return new EmptyTrashResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = source["items"];
	        this.bytes = source["bytes"];
	    }
	}
	
	export class RestoreProgress {
	    filesDone: number;
	    filesTotal: number;
	    bytesDone: number;
	    bytesTotal: number;
	    paused: boolean;
	    complete: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RestoreProgress(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filesDone = source["filesDone"];
	        this.filesTotal = source["filesTotal"];
	        this.bytesDone = source["bytesDone"];
	        this.bytesTotal = source["bytesTotal"];
	        this.paused = source["paused"];
	        this.complete = source["complete"];
	    }
	}
	export class Settings {
	    mountPoint: string;
	    deviceName: string;
	    uploads: number;
	    downloads: number;
	    activePollSeconds: number;
	    idlePollSeconds: number;
	    trashRetentionDays: number;
	    thumbnailerDenyList: string[];
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mountPoint = source["mountPoint"];
	        this.deviceName = source["deviceName"];
	        this.uploads = source["uploads"];
	        this.downloads = source["downloads"];
	        this.activePollSeconds = source["activePollSeconds"];
	        this.idlePollSeconds = source["idlePollSeconds"];
	        this.trashRetentionDays = source["trashRetentionDays"];
	        this.thumbnailerDenyList = source["thumbnailerDenyList"];
	    }
	}
	export class Status {
	    state: string;
	    vaultExists: boolean;
	    deviceName: string;
	    mountPoint: string;
	    online: boolean;
	    syncingItems: number;
	    notBackedUpFiles: number;
	    notBackedUpBytes: number;
	    // Go type: time
	    lastBackedUp: any;
	    bytesPerSecond: number;
	    corruptFiles: number;
	    restoreFailed: number;
	    keysMaySwap: boolean;
	    restore?: RestoreProgress;
	    message?: string;
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.vaultExists = source["vaultExists"];
	        this.deviceName = source["deviceName"];
	        this.mountPoint = source["mountPoint"];
	        this.online = source["online"];
	        this.syncingItems = source["syncingItems"];
	        this.notBackedUpFiles = source["notBackedUpFiles"];
	        this.notBackedUpBytes = source["notBackedUpBytes"];
	        this.lastBackedUp = this.convertValues(source["lastBackedUp"], null);
	        this.bytesPerSecond = source["bytesPerSecond"];
	        this.corruptFiles = source["corruptFiles"];
	        this.restoreFailed = source["restoreFailed"];
	        this.keysMaySwap = source["keysMaySwap"];
	        this.restore = this.convertValues(source["restore"], RestoreProgress);
	        this.message = source["message"];
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
	export class TrashItem {
	    id: string;
	    name: string;
	    originalPath: string;
	    reason: string;
	    device: string;
	    deviceName: string;
	    // Go type: time
	    deletedAt: any;
	    // Go type: time
	    expiresAt: any;
	    size: number;
	    fileCount: number;
	    folder: boolean;
	
	    static createFrom(source: any = {}) {
	        return new TrashItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.originalPath = source["originalPath"];
	        this.reason = source["reason"];
	        this.device = source["device"];
	        this.deviceName = source["deviceName"];
	        this.deletedAt = this.convertValues(source["deletedAt"], null);
	        this.expiresAt = this.convertValues(source["expiresAt"], null);
	        this.size = source["size"];
	        this.fileCount = source["fileCount"];
	        this.folder = source["folder"];
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
	export class TreeEntry {
	    name: string;
	    path: string;
	    folder: boolean;
	    size: number;
	    localSize: number;
	    state: string;
	    // Go type: time
	    modTime: any;
	
	    static createFrom(source: any = {}) {
	        return new TreeEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.folder = source["folder"];
	        this.size = source["size"];
	        this.localSize = source["localSize"];
	        this.state = source["state"];
	        this.modTime = this.convertValues(source["modTime"], null);
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

}

export namespace layout {
	
	export class Edge {
	    parent: string;
	    toRow: number;
	    toLane: number;
	    merge: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Edge(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.parent = source["parent"];
	        this.toRow = source["toRow"];
	        this.toLane = source["toLane"];
	        this.merge = source["merge"];
	    }
	}
	export class Row {
	    commit: api.CommitView;
	    lane: number;
	    colour: number;
	    retired: boolean;
	    merge: boolean;
	    edges: Edge[];
	
	    static createFrom(source: any = {}) {
	        return new Row(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.commit = this.convertValues(source["commit"], api.CommitView);
	        this.lane = source["lane"];
	        this.colour = source["colour"];
	        this.retired = source["retired"];
	        this.merge = source["merge"];
	        this.edges = this.convertValues(source["edges"], Edge);
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

}

export namespace main {
	
	export class HistoryPage {
	    rows: layout.Row[];
	    devices: api.DeviceView[];
	    nextBefore: string;
	
	    static createFrom(source: any = {}) {
	        return new HistoryPage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rows = this.convertValues(source["rows"], layout.Row);
	        this.devices = this.convertValues(source["devices"], api.DeviceView);
	        this.nextBefore = source["nextBefore"];
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

}

