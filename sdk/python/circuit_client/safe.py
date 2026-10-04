"""Small safety-aware facade over the generated agent API; no provider credentials."""
import json
import os
from pathlib import Path
import time
from urllib.parse import urlsplit

from urllib3.util import Retry
from circuit_client import ApiClient, Configuration, ActionsApi, ActionRequest, Action
from circuit_client.exceptions import ApiException


class ActionStopped(RuntimeError):
    def __init__(self, action):
        self.action = action
        super().__init__(f"Circuit action {action.id}: {action.state}: {action.reason}")


class Circuit:
    def __init__(self, url, token, ca_file=None):
        origin = urlsplit(url)
        if origin.scheme != 'https' or not origin.hostname or origin.username or origin.password or origin.path not in ('', '/') or origin.query or origin.fragment:
            raise ValueError('Circuit URL must be an HTTPS origin')
        if len(token) < 32 or '\n' in token or '\r' in token:
            raise ValueError('A scoped single-line agent token is required')
        config = Configuration(host=url.rstrip('/'), access_token=token, ssl_ca_cert=ca_file)
        config.retries = Retry(total=0, redirect=0, raise_on_redirect=False)
        self.client = ApiClient(config)
        self.api = ActionsApi(self.client)

    @classmethod
    def from_environment(cls):
        return cls(os.environ['CIRCUIT_GATEWAY_URL'],
                   Path(os.environ['CIRCUIT_TOKEN_FILE']).read_text().strip(),
                   os.environ.get('CIRCUIT_CA_CERT'))

    def close(self):
        self.client.rest_client.pool_manager.clear()

    def submit(self, operation, target, args, key):
        try:
            return self.api.submit_action(key, ActionRequest(operation=operation, custom_tool=target, args=args), _request_timeout=30)
        except ApiException as error:
            # Denied/expired actions are data, unlike authentication or transport failures.
            if error.status in (403, 410) and error.body:
                data = json.loads(error.body)
                if 'id' in data and 'state' in data:
                    return Action.from_dict(data)
            raise

    def get(self, action_id):
        return self.api.get_action(action_id, _request_timeout=30)

    def execute(self, operation, target, args, key, timeout=60, poll_interval=1):
        if timeout <= 0 or poll_interval <= 0:
            raise ValueError('Positive timeout and poll interval are required')
        deadline = time.monotonic()+timeout
        action = self.submit(operation, target, args, key)
        while action.state in ('pending', 'approved', 'executing'):
            remaining = deadline-time.monotonic()
            if remaining <= 0:
                raise ActionStopped(action)
            time.sleep(min(poll_interval, remaining))
            action = self.get(action.id)
        if action.state != 'succeeded':
            raise ActionStopped(action)
        return action
