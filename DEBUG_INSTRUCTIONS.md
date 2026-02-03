# Atualização de Webhook Chatwoot e Debug

## Alterações Realizadas

1.  **Flexibilidade no Payload**:
    *   O campo `message_type` agora aceita `string` ou `int/float`.
    *   Isso resolve problemas onde algumas versões do Chatwoot enviam `0` (incoming) ou `1` (outgoing) em vez de "incoming"/"outgoing".
    *   Implementada função auxiliar `getString()` para normalizar esses valores.

2.  **Nova Rota de Debug**:
    *   `POST /chatwoot/payload-test`
    *   Esta rota aceita qualquer payload JSON e retorna uma análise detalhada.
    *   Útil para verificar exatamente o que o Chatwoot está enviando sem precisar olhar logs do container.

## Como Testar (Quando for compilar)

### 1. Rebuild do Container
```bash
docker-compose -f docker-compose-swarm.yaml build wuzapi
docker-compose -f docker-compose-swarm.yaml up -d wuzapi
```

### 2. Testar Payload (Via Postman ou Insomnia)

Faça uma requisição `POST` para `https://seu-dominio/chatwoot/payload-test` com o JSON que você suspeita estar errado.

**Exemplo de Resposta de Sucesso:**
```json
{
  "analysis": {
    "event": "message_created",
    "message_type": "incoming",
    "private": false,
    "content": "Teste",
    "account_id": 1,
    "inbox_id": 3
  },
  "headers": { ... },
  "parsed_struct": { ... },
  "raw_body": "..."
}
```

### 3. Verificar Logs em Tempo Real
```bash
docker service logs -f deskwuzapi_wuzapi
```
