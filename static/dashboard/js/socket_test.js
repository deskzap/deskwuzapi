
// Socket.IO Test Logic

let socket = null;
let currentWebhookData = {
    webhook: "",
    events: []
};

function initSocketDebugger() {
  // Bind Card Click
  const card = document.getElementById('socketConfig');
  if (card) {
      card.addEventListener('click', function() {
        $('#modalSocketDebugger').modal('show');
        // Initialize Tabs
        $('#modalSocketDebugger .menu .item').tab();
        
        checkSocketStatus();
        fetchSocketConfig();
      });
  }

  // Bind Buttons
  const btnConnect = document.getElementById('btnConnectSocket');
  if(btnConnect) btnConnect.addEventListener('click', toggleSocketConnection);
  
  const btnClear = document.getElementById('btnClearLog');
  if(btnClear) btnClear.addEventListener('click', () => {
      document.getElementById('socketLog').innerHTML = '';
  });
  
  const btnSend = document.getElementById('btnSocketSend');
  if(btnSend) btnSend.addEventListener('click', sendSocketMessage);
  
  const btnSave = document.getElementById('btnSaveSocketConfig');
  if(btnSave) btnSave.addEventListener('click', saveSocketConfig);

  // Bind "All" Checkbox
  const chkAll = document.querySelector('input[name="ws_event_all"]');
  if (chkAll) {
      chkAll.addEventListener('change', function() {
          const checkboxes = document.querySelectorAll('input[name="ws_event"]');
          checkboxes.forEach(cb => {
              cb.checked = this.checked;
              if(this.checked) cb.parentElement.classList.add('checked');
              else cb.parentElement.classList.remove('checked');
          });
      });
  }
}

function checkSocketStatus() {
    const statusLabel = document.getElementById('socketStatusLabel');
    const btn = document.getElementById('btnConnectSocket');
    
    if(!statusLabel || !btn) return;

    if (socket && socket.connected) {
        statusLabel.className = "ui green label";
        statusLabel.innerText = "Connected";
        btn.className = "ui red button";
        btn.innerText = "Disconnect";
    } else {
        statusLabel.className = "ui red label";
        statusLabel.innerText = "Disconnected";
        btn.className = "ui green button";
        btn.innerText = "Connect";
    }
}

function logSocketEvent(type, data) {
    const logContainer = document.getElementById('socketLog');
    if(!logContainer) return;
    
    const entry = document.createElement('div');
    entry.style.borderBottom = "1px solid #eee";
    entry.style.padding = "5px";
    
    const time = new Date().toLocaleTimeString();
    const dataStr = typeof data === 'object' ? JSON.stringify(data, null, 2) : data;
    
    let color = 'black';
    if (type === 'INCOMING') color = 'blue';
    if (type === 'OUTGOING') color = 'green';
    if (type === 'ERROR') color = 'red';
    if (type === 'SYSTEM') color = 'grey';
    if (type === 'ACK') color = 'purple';

    entry.innerHTML = `<span style="color:${color}; font-weight:bold">[${time}] ${type}:</span> <pre style="margin:0; font-size:0.9em">${dataStr}</pre>`;
    
    logContainer.prepend(entry);
}

function toggleSocketConnection() {
    if (socket && socket.connected) {
        socket.disconnect();
        checkSocketStatus();
        logSocketEvent('SYSTEM', 'Disconnected manually');
    } else {
        connectSocket();
    }
}

function connectSocket() {
    // Rely on app.js helper
    const token = getLocalStorageItem('token');
    
    if (!token) {
        alert("No token found. Please login.");
        return;
    }

    // Using query param for auth as implemented in backend
    const url = window.location.protocol + '//' + window.location.host; 
    
    logSocketEvent('SYSTEM', `Connecting to ${url}...`);

    socket = io(url, {
        query: { token: token },
        transports: ['websocket', 'polling'],
        reconnection: true,           // Enable auto-reconnect
        reconnectionAttempts: 10,     // Max attempts
        reconnectionDelay: 1000,       // Initial delay (ms)
        reconnectionDelayMax: 30000,   // Max delay (30s)
        randomizationFactor: 0.5,      // Randomization to avoid thundering herd
        timeout: 20000                 // Connection timeout (20s)
    });

    socket.on('connect', () => {
        checkSocketStatus();
        logSocketEvent('SYSTEM', 'Connected! ID: ' + socket.id);
    });

    socket.on('disconnect', (reason) => {
        checkSocketStatus();
        logSocketEvent('SYSTEM', 'Disconnected: ' + reason);
        if (reason === 'io server disconnect') {
            // Server initiated disconnect - manual reconnect needed
            logSocketEvent('SYSTEM', 'Server disconnected. Click Connect to retry.');
        }
    });

    socket.on('reconnecting', (attemptNumber) => {
        logSocketEvent('SYSTEM', `Reconnecting... Attempt ${attemptNumber}`);
    });

    socket.on('reconnect', (attemptNumber) => {
        checkSocketStatus();
        logSocketEvent('SYSTEM', `Reconnected after ${attemptNumber} attempts!`);
    });

    socket.on('reconnect_error', (error) => {
        logSocketEvent('ERROR', 'Reconnect error: ' + error.message);
    });

    socket.on('reconnect_failed', () => {
        logSocketEvent('ERROR', 'Reconnection failed after max attempts. Click Connect to retry.');
        checkSocketStatus();
    });

    socket.on('error', (error) => {
        logSocketEvent('ERROR', error);
    });
    
    socket.on('connect_error', (error) => {
        logSocketEvent('ERROR', 'Connect Error: ' + error.message);
    });

    // Listen for custom events broadcasted from backend
    socket.on('events', (data) => {
        // data might be JSON string or object depending on backend broadcast
        logSocketEvent('INCOMING (Event)', data);
    });

    // CRITICAL: Listen for 'message' event (Standard Broadcast)
    socket.on('message', (data) => {
        logSocketEvent('INCOMING (Message)', data);
    });
}

function sendSocketMessage() {
    if (!socket || !socket.connected) {
        alert("Socket not connected");
        return;
    }

    const phone = document.getElementById('socketTestPhone').value;
    const msg = document.getElementById('socketTestMsg').value;

    if (!phone || !msg) {
        alert("Phone and Message required");
        return;
    }

    const payload = JSON.stringify({
        phone: phone,
        message: msg
    });

    logSocketEvent('OUTGOING', payload);
    
    socket.emit('message', payload, (response) => {
        // Ack callback
        logSocketEvent('ACK', response);
    });
}

// Config Functions

function fetchSocketConfig() {
    const token = getLocalStorageItem('token');
    if (!token) return;

    fetch('/webhook', {
        headers: { 'token': token }
    })
    .then(response => response.json())
    .then(responseData => {
        console.log("Socket Config Raw Response:", responseData);
        
        // Handle nested data structure: { code: 200, success: true, data: {...} }
        const data = responseData.data || responseData;
        console.log("Socket Config Data:", data);
        
        currentWebhookData.webhook = data.webhook || "";
        currentWebhookData.events = data.subscribe || [];
        
        // Populate UI
        const enabled = data.websocket_enabled !== false; // Default true if undefined
        $('input[name="websocket_enabled"]').prop('checked', enabled);
        
        const wsEvents = data.websocket_events || [];
        console.log("WebSocket Events from API:", wsEvents);
        
        if (wsEvents.includes('All')) {
            $('input[name="ws_event_all"]').prop('checked', true).parent().addClass('checked');
            $('input[name="ws_event"]').prop('checked', true).parent().addClass('checked');
        } else {
            $('input[name="ws_event_all"]').prop('checked', false).parent().removeClass('checked');
            $('input[name="ws_event"]').each(function() {
                const isChecked = wsEvents.includes(this.value);
                $(this).prop('checked', isChecked);
                if (isChecked) {
                    $(this).parent().addClass('checked');
                } else {
                    $(this).parent().removeClass('checked');
                }
            });
        }
    })
    .catch(err => console.error("Failed to fetch webhook config", err));
}

function saveSocketConfig() {
    const token = getLocalStorageItem('token');
    if (!token) return;

    const enabled = $('input[name="websocket_enabled"]').is(':checked');
    const allEvents = $('input[name="ws_event_all"]').is(':checked');
    let wsEvents = [];
    
    if (allEvents) {
        wsEvents = ['All'];
    } else {
        $('input[name="ws_event"]:checked').each(function() {
            wsEvents.push(this.value);
        });
    }

    // If no events selected, default to empty array (will save as empty in DB)
    // If empty, maybe set to 'All' or keep empty based on preference
    if (wsEvents.length === 0) {
        wsEvents = []; // Empty means no events
    }

    const payload = {
        webhook: currentWebhookData.webhook,
        events: currentWebhookData.events,
        websocket_enabled: enabled,
        websocket_events: wsEvents
    };

    console.log("Saving WebSocket Config:", payload);

    const btn = document.getElementById('btnSaveSocketConfig');
    const msgDiv = document.getElementById('socketConfigMsg');
    
    btn.classList.add('loading');
    
    fetch('/webhook', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'token': token
        },
        body: JSON.stringify(payload)
    })
    .then(response => {
        console.log("Response status:", response.status);
        if (!response.ok) throw new Error("Failed to save");
        return response.json();
    })
    .then(data => {
        console.log("Response data:", data);
        msgDiv.innerHTML = '<div class="ui green text">Configuration saved!</div>';
        setTimeout(() => msgDiv.innerHTML = '', 3000);
    })
    .catch(err => {
        msgDiv.innerHTML = '<div class="ui red text">Error saving configuration</div>';
        console.error(err);
    })
    .finally(() => {
        btn.classList.remove('loading');
    });
}

// Auto init
document.addEventListener('DOMContentLoaded', initSocketDebugger);
