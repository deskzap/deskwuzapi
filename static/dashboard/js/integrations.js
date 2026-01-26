
// Integrations Management Logic
console.log("Integrations script loaded");



function initIntegrations() {
  const configCard = document.getElementById('integrationsConfig');
  if (configCard) {
      configCard.addEventListener('click', async function() {
        // Show modal first with loading state, then load data
        const tbody = document.getElementById('integrationsTableBody');
        tbody.innerHTML = '<tr><td colspan="6" class="center aligned"><div class="ui active inline loader"></div> Loading integrations...</td></tr>';
        $('#modalIntegrationsList').modal('show');
        
        // Now load the actual data
        await loadIntegrations();
      });
  }

  // Bind Add Integration button
  const btnAdd = document.getElementById('btnAddIntegration');
  if (btnAdd) {
     btnAdd.addEventListener('click', function() {
        openIntegrationEditor();
     });
  }

  // Bind Type change to show/hide fields
  $('#integrationType').dropdown({
    onChange: function(value, text, $selectedItem) {
      handleIntegrationTypeChange(value);
    }
  });

  // Bind Save button
  const btnSave = document.getElementById('btnSaveIntegration');
  if (btnSave) btnSave.addEventListener('click', saveIntegration);
  
  // Initialize dropdowns
  $('#integrationEvents').dropdown();
}

function handleIntegrationTypeChange(type) {
  const chatwootFields = document.getElementById('chatwootFields');
  if (type === 'chatwoot') {
    chatwootFields.style.display = 'block';
  } else {
    chatwootFields.style.display = 'none';
  }
}

async function loadIntegrations() {
  const tbody = document.getElementById('integrationsTableBody');
  // Show loading state
  tbody.innerHTML = '<tr><td colspan="6" class="center aligned"><div class="ui active inline loader"></div> Loading integrations...</td></tr>';
  $('#modalIntegrationsList').modal('refresh');

  const token = getLocalStorageItem('token');
  const myHeaders = new Headers();
  myHeaders.append('token', token);

  try {
    const response = await fetch(baseUrl + "/session/integrations", {
      method: "GET",
      headers: myHeaders
    });
    const result = await response.json();
    
    tbody.innerHTML = ''; // Clear loading
    
    // Extract integrations from response (API wraps in data object)
    const integrations = result.data?.integrations || result.integrations || [];
    console.log("Integrations loaded:", integrations.length);

    if (integrations.length > 0) {
      integrations.forEach(integration => {
        const tr = document.createElement('tr');
        
        // Parse Meta for display extra info (e.g. Chatwoot inbox)
        let extraInfo = '';
        if (integration.type === 'chatwoot' && integration.meta) {
            try {
                const meta = JSON.parse(integration.meta);
                extraInfo = `<br><small class="text-muted">Inbox ID: ${meta.inbox_id || 'Pending'}</small>`;
            } catch(e) {}
        }

        tr.innerHTML = `
          <td>${integration.id}</td>
          <td>${integration.name}</td>
          <td><div class="ui label">${integration.type}</div>${extraInfo}</td>
          <td>${integration.url}</td>
          <td>
             <div class="ui toggle checkbox">
                <input type="checkbox" ${integration.status ? 'checked' : ''} onchange="toggleIntegrationStatus(${integration.id}, this.checked)">
                <label></label>
             </div>
          </td>
          <td>
            ${integration.type === 'chatwoot' ? 
                `<button class="ui tiny icon purple button" onclick="testIntegration(${integration.id})" title="Test Connection">
                  <i class="plug icon"></i>
                </button>` : ''}
            <button class="ui tiny icon button" onclick="editIntegration(${integration.id})" title="Edit">
              <i class="edit icon"></i>
            </button>
            <button class="ui tiny icon red button" onclick="deleteIntegration(${integration.id})" title="Delete">
              <i class="trash icon"></i>
            </button>
          </td>
        `;
        tbody.appendChild(tr);
      });
    } else {
      tbody.innerHTML = '<tr><td colspan="6" class="center aligned">No integrations found</td></tr>';
    }
    
    // Refresh modal position after content update
    $('#modalIntegrationsList').modal('refresh');

  } catch (error) {
    console.error('Error loading integrations:', error);
    tbody.innerHTML = `<tr><td colspan="6" class="center aligned error"><i class="exclamation triangle icon"></i> Failed to load integrations: ${error.message}</td></tr>`;
    $('#modalIntegrationsList').modal('refresh');
    showError('Failed to load integrations');
  }
}

function openIntegrationEditor(mode = 'create', data = null) {
  // Reset form
  document.getElementById('integrationForm').reset();
  $('#integrationType').dropdown('clear');
  $('#integrationEvents').dropdown('clear');
  document.getElementById('chatwootFields').style.display = 'none';
  document.getElementById('cwWebhookInfo').style.display = 'none';
  
  document.getElementById('integrationId').value = '';
  document.getElementById('integrationMode').value = mode;

  if (mode === 'edit' && data) {
    document.getElementById('integrationId').value = data.id;
    document.getElementById('integrationName').value = data.name;
    $('#integrationType').dropdown('set selected', data.type);
    // Force trigger type change handler to ensure UI updates
    handleIntegrationTypeChange(data.type);

    document.getElementById('integrationUrl').value = data.url;
    document.getElementById('integrationToken').value = data.token;
    
    if (data.events) {
        $('#integrationEvents').dropdown('set selected', data.events.split(','));
    }

    if (data.type === 'chatwoot' && data.meta) {
         try {
             const meta = JSON.parse(data.meta);
             document.getElementById('cwUrl').value = data.url || '';
             document.getElementById('cwAccountId').value = meta.account_id || '';
             document.getElementById('cwToken').value = meta.token || '';
             document.getElementById('cwInboxName').value = meta.inbox_name || '';
             document.getElementById('cwInboxId').value = meta.inbox_id || '';
             document.getElementById('cwOrganization').value = meta.organization || '';
             document.getElementById('cwLogo').value = meta.logo || '';
             document.getElementById('cwSignDelimiter').value = meta.sign_delimiter || '\\n';
             document.getElementById('cwDaysLimitImportMessages').value = meta.import_days || 3;
             document.getElementById('cwIgnoreJids').value = meta.ignore_jids || '';

             document.getElementById('cwEnabled').checked = meta.enabled !== false;
             document.getElementById('cwAutoCreate').checked = meta.auto_create === true;
             document.getElementById('cwSignMessages').checked = meta.sign_messages || false;
             document.getElementById('cwConversationPending').checked = meta.conversation_pending || false;
             document.getElementById('cwReopenConversation').checked = meta.reopen_conversation || false;
             document.getElementById('cwImportContacts').checked = meta.import_contacts || false;
             document.getElementById('cwImportMessages').checked = meta.import_messages || false;
             document.getElementById('cwMergeBrazilContacts').checked = meta.merge_brazil_contacts || false;

             // Show webhook URL if integration has an ID
             if (data.id) {
                 // Get instance name from session storage or use default
                 const instanceName = getLocalStorageItem('userName') || 'instance';
                 // New Evolution API compatible URL format
                 const webhookUrl = `${window.location.origin}/chatwoot/webhook/${instanceName}`;
                 document.getElementById('cwWebhookUrl').value = webhookUrl;
                 document.getElementById('cwWebhookInfo').style.display = 'block';
             }
         } catch(e) { console.error("Error parsing Chatwoot meta", e); }
    }
  }

  $('#modalIntegrationEditor').modal('show');
}

async function editIntegration(id) {
    const token = getLocalStorageItem('token');
    const myHeaders = new Headers();
    myHeaders.append('token', token);
    
    try {
        const response = await fetch(baseUrl + "/session/integrations", {
          method: "GET",
          headers: myHeaders
        });
        const result = await response.json();
        // API returns data.integrations
        const integrations = result.data?.integrations || result.integrations || [];
        const integration = integrations.find(i => i.id === id);
        if (integration) {
            openIntegrationEditor('edit', integration);
        } else {
            showError("Integration not found");
        }
    } catch(e) {
        console.error("Edit integration error:", e);
        showError("Failed to load integration details");
    }
}


async function saveIntegration() {
  const mode = document.getElementById('integrationMode').value;
  const id = document.getElementById('integrationId').value;
  
  const name = document.getElementById('integrationName').value;
  const type = $('#integrationType').dropdown('get value');
  let url = document.getElementById('integrationUrl').value;
  const tokenStr = document.getElementById('integrationToken').value; // Integration Token (optional)
  const events = $('#integrationEvents').dropdown('get value'); // returns array
  const eventsStr = Array.isArray(events) ? events.join(',') : events;

  // Chatwoot Override Logic
  if (type === 'chatwoot') {
    url = document.getElementById('cwUrl').value;
    if (!url) {
        showError("Chatwoot URL is required");
        return;
    }
  }

  if (!name || !type || !url || !eventsStr) {
    showError("Please fill all required fields (" + (!name?"Name ":"") + (!type?"Type ":"") + (!url?"URL ":"") + (!eventsStr?"Events":"") + ")");
    return;
  }

  // Build Payload
  const payload = {
    name: name,
    type: type,
    url: url,
    token: tokenStr,
    events: eventsStr,
    status: true // Default active
  };

  if (type === 'chatwoot') {
      const cwAccountId = document.getElementById('cwAccountId').value;
      const cwToken = document.getElementById('cwToken').value; // Chatwoot API Token
      
      // Validation
      if (!cwAccountId || !cwToken) {
          showError("Chatwoot Account ID and Token are required");
          return;
      }

      const cwEnabled = document.getElementById('cwEnabled').checked;
      const cwInboxName = document.getElementById('cwInboxName').value;
      const cwInboxId = document.getElementById('cwInboxId').value;
      const cwOrganization = document.getElementById('cwOrganization').value;
      const cwLogo = document.getElementById('cwLogo').value;
      const cwSignMessages = document.getElementById('cwSignMessages').checked;
      const cwSignDelimiter = document.getElementById('cwSignDelimiter').value;
      const cwAutoCreate = document.getElementById('cwAutoCreate').checked;
      const cwConversationPending = document.getElementById('cwConversationPending').checked;
      const cwReopenConversation = document.getElementById('cwReopenConversation').checked;
      const cwImportContacts = document.getElementById('cwImportContacts').checked;
      const cwImportMessages = document.getElementById('cwImportMessages').checked;
      const cwDaysLimitImportMessages = document.getElementById('cwDaysLimitImportMessages').value;
      const cwIgnoreJids = document.getElementById('cwIgnoreJids').value;
      const cwMergeBrazilContacts = document.getElementById('cwMergeBrazilContacts').checked;

      payload.meta = {
          enabled: cwEnabled,
          url: url, // Explicitly include URL in meta
          account_id: cwAccountId,
          token: cwToken,
          inbox_name: cwInboxName,
          inbox_id: cwInboxId ? parseInt(cwInboxId) : 0,
          organization: cwOrganization,
          logo: cwLogo,
          sign_messages: cwSignMessages,
          sign_delimiter: cwSignDelimiter,
          auto_create: cwAutoCreate,
          conversation_pending: cwConversationPending,
          reopen_conversation: cwReopenConversation,
          import_contacts: cwImportContacts,
          import_messages: cwImportMessages,
          import_days: cwDaysLimitImportMessages ? parseInt(cwDaysLimitImportMessages) : 3,
          ignore_jids: cwIgnoreJids,
          merge_brazil_contacts: cwMergeBrazilContacts
      };
      
      console.log('Sending Chatwoot Payload:', payload);
  }
  
  const endpoint = mode === 'edit' ? `/session/integrations/${id}` : '/session/integrations';
  const method = mode === 'edit' ? 'PUT' : 'POST';

  const token = getLocalStorageItem('token');
  const myHeaders = new Headers();
  myHeaders.append('token', token);
  myHeaders.append('Content-Type', 'application/json');

  try {
    const response = await fetch(baseUrl + endpoint, {
      method: method,
      headers: myHeaders,
      body: JSON.stringify(payload)
    });
    
    if (response.ok) {
        $('#modalIntegrationEditor').modal('hide');
        showSuccess(`Integration ${mode === 'edit' ? 'updated' : 'saved'} successfully`);
        loadIntegrations();
    } else {
        const err = await response.json();
        showError("Error saving: " + (err.error || response.statusText));
    }
  } catch (error) {
    showError("Network error");
  }
}

async function deleteIntegration(id) {
    if (!confirm("Are you sure you want to delete this integration?")) return;
    
    const token = getLocalStorageItem('token');
    const myHeaders = new Headers();
    myHeaders.append('token', token);

    try {
        const response = await fetch(baseUrl + `/session/integrations/${id}`, {
            method: "DELETE",
            headers: myHeaders
        });
        if (response.ok) {
            showSuccess("Integration deleted");
            loadIntegrations();
        } else {
            showError("Failed to delete");
        }
    } catch (e) {
        showError("Network error");
    }
}

async function testIntegration(id) {
    const token = getLocalStorageItem('token');
    const myHeaders = new Headers();
    myHeaders.append('token', token);

    // Find button to show loading
    const btn = document.querySelector(`button[onclick="testIntegration(${id})"]`);
    if(btn) btn.classList.add('loading', 'disabled');

    try {
        const response = await fetch(baseUrl + `/session/integrations/${id}/test`, {
            method: "POST",
            headers: myHeaders
        });
        const result = await response.json();
        
        if (response.ok) {
            if (result.success) {
                showSuccess(result.message || "Connection successful!");
            } else {
                showError(result.message || "Connection failed");
            }
        } else {
            showError(result.error || "Failed to test connection");
        }
    } catch (e) {
        console.error("Test integration error:", e);
        showError("Network error during test");
    } finally {
        if(btn) btn.classList.remove('loading', 'disabled');
    }
}

async function toggleIntegrationStatus(id, newStatus) {
    const token = getLocalStorageItem('token');
    const myHeaders = new Headers();
    myHeaders.append('token', token);
    myHeaders.append('Content-Type', 'application/json');

    try {
        // First, get the current integration data
        const getResponse = await fetch(baseUrl + "/session/integrations", {
            method: "GET",
            headers: myHeaders
        });
        const result = await getResponse.json();
        const integrations = result.data?.integrations || result.integrations || [];
        const integration = integrations.find(i => i.id === id);
        
        if (!integration) {
            showError("Integration not found");
            return;
        }

        // Update only the status field
        const payload = {
            name: integration.name,
            url: integration.url,
            token: integration.token || "",
            events: integration.events || "",
            status: newStatus,
            meta: integration.meta ? JSON.parse(integration.meta) : {}
        };

        const updateResponse = await fetch(baseUrl + `/session/integrations/${id}`, {
            method: "PUT",
            headers: myHeaders,
            body: JSON.stringify(payload)
        });

        if (updateResponse.ok) {
            showSuccess(`Integration ${newStatus ? 'enabled' : 'disabled'}`);
        } else {
            showError("Failed to update status");
            loadIntegrations(); // Reload to reset the checkbox
        }
    } catch (e) {
        console.error("Toggle status error:", e);
        showError("Network error");
        loadIntegrations(); // Reload to reset the checkbox
    }
}

// Copy webhook URL to clipboard
function copyWebhookUrl() {
    const webhookInput = document.getElementById('cwWebhookUrl');
    webhookInput.select();
    webhookInput.setSelectionRange(0, 99999); // For mobile devices
    
    try {
        navigator.clipboard.writeText(webhookInput.value).then(() => {
            showSuccess('Webhook URL copied to clipboard!');
        }).catch(err => {
            // Fallback for older browsers
            document.execCommand('copy');
            showSuccess('Webhook URL copied!');
        });
    } catch (err) {
        document.execCommand('copy');
        showSuccess('Webhook URL copied!');
    }
}

document.addEventListener('DOMContentLoaded', initIntegrations);
