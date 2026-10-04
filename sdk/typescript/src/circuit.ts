import { ActionsApi } from './apis/ActionsApi';
import { Configuration, ResponseError } from './runtime';
import { Action, ActionFromJSON } from './models/Action';
import { ActionRequest } from './models/ActionRequest';

export class ActionStopped extends Error {
    constructor(public readonly action: Action) {
        super(`Circuit action ${action.id}: ${action.state}: ${action.reason}`);
        Object.setPrototypeOf(this, ActionStopped.prototype);
    }
}

export class Circuit {
    private readonly api: ActionsApi;

    constructor(options: { url: string; token: string; fetchApi?: typeof fetch }) {
        const url = new URL(options.url);
        if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash || (url.pathname !== '/' && url.pathname !== '')) {
            throw new Error('Circuit URL must be an HTTPS origin');
        }
        if (options.token.length < 32 || /[\r\n]/.test(options.token)) {
            throw new Error('A scoped single-line agent token is required');
        }
        const transport = options.fetchApi || fetch;
        this.api = new ActionsApi(new Configuration({
            basePath: url.origin, accessToken: options.token,
            fetchApi: async (input, init) => {
                const controller = new AbortController();
                const timer = setTimeout(() => controller.abort(), 30000);
                try { return await transport(input, { ...init, redirect: 'error', signal: controller.signal }); }
                finally { clearTimeout(timer); }
            },
        }));
    }

    async submit(request: ActionRequest, key: string): Promise<Action> {
        try { return await this.api.submitAction({ actionRequest: request, idempotencyKey: key }); }
        catch (error) {
            if (error instanceof ResponseError && [403, 410].indexOf(error.response.status) !== -1) {
                const body = await error.response.json();
                if (body.id && body.state) { return ActionFromJSON(body); }
            }
            throw error;
        }
    }

    async get(id: string): Promise<Action> { return this.api.getAction({ id }); }

    async execute(request: ActionRequest, key: string, timeoutMs = 60000, pollMs = 1000): Promise<Action> {
        if (timeoutMs <= 0 || pollMs <= 0) { throw new Error('Positive timeout and poll interval are required'); }
        const deadline = Date.now() + timeoutMs;
        let action = await this.submit(request, key);
        while (['pending', 'approved', 'executing'].indexOf(action.state) !== -1) {
            const remaining = deadline - Date.now();
            if (remaining <= 0) { throw new ActionStopped(action); }
            await new Promise(resolve => setTimeout(resolve, Math.min(pollMs, remaining)));
            action = await this.get(action.id);
        }
        if (action.state !== 'succeeded') { throw new ActionStopped(action); }
        return action;
    }
}
