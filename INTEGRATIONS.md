# Documentação Técnica de Integrações

Este documento detalha como o **Wuzapi** gerencia integrações externas, focando especificamente na compatibilidade com o **Chatwoot**.

> **Nota**: Estes endpoints são de uso interno para o funcionamento das integrações e não se destinam ao consumo direto por usuários finais (diferente da `API.md`).

---

## 1. Integração com Chatwoot

O Wuzapi atua como um **Canal de API (API Channel)** para o Chatwoot.

### 1.1 Fluxo: Wuzapi → Chatwoot (Entrada de Mensagens)
Quando uma mensagem é recebida no WhatsApp, o Wuzapi a encaminha para o Chatwoot convertendo os dados para o formato esperado pela API do Chatwoot.

**Mapeamento de Ações:**
1.  **Buscar/Criar Contato**:
    *   **Endpoint Chatwoot**: `POST /api/v1/accounts/{id}/contacts`
    *   **Identificador Único**: O `identifier` é o JID do WhatsApp (ex: `5511999999999@s.whatsapp.net`).
2.  **Buscar/Criar Conversa**:
    *   **Endpoint Chatwoot**: `POST /api/v1/accounts/{id}/conversations`
    *   **Lógica**: Busca uma conversa com status `open`. Se não existir, cria uma nova.
3.  **Enviar Mensagem**:
    *   **Endpoint Chatwoot**: `POST /api/v1/accounts/{id}/conversations/{conv_id}/messages`
    *   **Suporte a Mídia**: Imagens, Áudio, Vídeo e Arquivos são enviados via `multipart/form-data`.

### 1.2 Fluxo: Chatwoot → Wuzapi (Saída de Mensagens)
O Chatwoot notifica o Wuzapi via Webhook quando um agente responde.

**Endpoint de Recebimento (Wuzapi)**:
`POST /chatwoot/webhook/{integration_id}`

**Payload Recebido (Formato Chatwoot):**
O Wuzapi espera e processa o seguinte payload JSON (apenas campos relevantes exibidos):

```json
{
  "event": "message_created",
  "message_type": "outgoing",
  "private": false,
  "content": "Olá, como posso ajudar?",
  "conversation": {
    "meta": {
      "contact": {
        "identifier": "5511999999999@s.whatsapp.net",  <-- CRÍTICO: Usado para identificar o destino
        "phone_number": "+5511999999999"               <-- Fallback
      }
    }
  },
  "attachments": [
    {
      "data_url": "https://...",
      "file_type": "image"
    }
  ]
}
```

**Lógica de Processamento:**
1.  **Validação**: O evento deve ser `message_created` e o tipo `outgoing`. Mensagens privadas são ignoradas.
2.  **Roteamento**: O sistema extrai o JID do campo `conversation.meta.contact.identifier`.
3.  **Envio**: A mensagem (texto ou anexo) é enviada para a fila de mensagens do WhatsApp.

---

## 2. Configurações Especiais

### 2.1 Bypass de WAF (Cloudflare)
Para garantir que os testes de conexão e chamadas de API funcionem mesmo quando o Chatwoot está protegido por WAF (como Cloudflare), o Wuzapi utiliza o seguinte cabeçalho:

```http
User-Agent: Mozilla/5.0 (Compatible; Wuzapi/1.0)
```

### 2.2 Contato "Manager"
O sistema cria automaticamente um contato especial `+123456` "Manager". Mensagens enviadas para este contato são interceptadas pelo Wuzapi para executar comandos de sistema (ex: reconectar sessão), sem serem enviadas ao WhatsApp.
