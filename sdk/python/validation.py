from circuit_client.safe import Circuit, ActionStopped

circuit = Circuit.from_environment()
try:
    assert circuit.execute('lookup', 'inventory', {'sku': 'python'}, 'python-read').state == 'succeeded'
    pending = circuit.submit('reserve', 'inventory', {'sku': 'python'}, 'python-write')
    assert pending.state == 'pending'
    assert circuit.execute('reserve', 'inventory', {'sku': 'python'}, 'python-write').id == pending.id
    for operation, key, state in [('outside_scope', 'python-denied', 'denied'), ('lose', 'python-uncertain', 'uncertain'), ('lose', 'python-uncertain', 'uncertain')]:
        try:
            circuit.execute(operation, 'inventory', {'sku': 'python'}, key)
        except ActionStopped as error:
            assert error.action.state == state
        else:
            raise AssertionError('non-success must stop automation')
    print('PASS Python: verified TLS, read, exact approval, denial and uncertain no-replay')
finally:
    circuit.close()
