# 🌐 Frontend Connection Guide

## 🎯 **Load Balancer Host Configuration**

Your Enterprise Load Balancer is running and ready for frontend connections!

### **📡 Current Host Settings:**

#### **Main Load Balancer:**
```
Host: localhost
Port: 8080
URL: http://localhost:8080
```

#### **Backend Servers:**
```
Backend 1: http://localhost:8081
Backend 2: http://localhost:8082  
Backend 3: http://localhost:8083
```

### **🔧 Frontend Connection Options:**

#### **Option 1: Direct Connection**
```javascript
// Frontend JavaScript
const API_BASE_URL = 'http://localhost:8080';

// Example API call
fetch(`${API_BASE_URL}/api/users`)
  .then(response => response.json())
  .then(data => console.log(data));
```

#### **Option 2: Environment Variables**
```javascript
// .env file
REACT_APP_API_BASE_URL=http://localhost:8080
VUE_APP_API_BASE_URL=http://localhost:8080
ANGULAR_APP_API_BASE_URL=http://localhost:8080

// Frontend code
const API_BASE_URL = process.env.REACT_APP_API_BASE_URL;
```

#### **Option 3: Configuration File**
```javascript
// config.js
const config = {
  API_BASE_URL: 'http://localhost:8080',
  WEBSOCKET_URL: 'ws://localhost:8081',
  METRICS_URL: 'http://localhost:9090'
};

export default config;
```

### **🌍 Network Configuration**

#### **Local Development:**
```bash
# Frontend running on different port
Frontend: http://localhost:3000
Load Balancer: http://localhost:8080
```

#### **Network Access:**
```bash
# Allow external connections (if needed)
# Update config.json to bind to 0.0.0.0
{
  "host": "0.0.0.0",
  "port": 8080
}
```

#### **CORS Configuration:**
```json
// Add to config.json
{
  "cors": {
    "enabled": true,
    "allowed_origins": ["http://localhost:3000", "http://localhost:8080"],
    "allowed_methods": ["GET", "POST", "PUT", "DELETE"],
    "allowed_headers": ["Content-Type", "Authorization"]
  }
}
```

### **📱 Frontend Examples**

#### **React.js:**
```jsx
// api.js
const API_BASE_URL = 'http://localhost:8080';

export const api = {
  getUsers: () => fetch(`${API_BASE_URL}/api/users`),
  createUser: (user) => fetch(`${API_BASE_URL}/api/users`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(user)
  })
};

// App.js
import { api } from './api';

function App() {
  const [users, setUsers] = useState([]);
  
  useEffect(() => {
    api.getUsers().then(res => res.json()).then(setUsers);
  }, []);
  
  return <div>{/* Your UI */}</div>;
}
```

#### **Vue.js:**
```javascript
// services/api.js
const API_BASE_URL = 'http://localhost:8080';

export default {
  getUsers() {
    return fetch(`${API_BASE_URL}/api/users`).then(res => res.json());
  },
  
  createUser(user) {
    return fetch(`${API_BASE_URL}/api/users`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(user)
    }).then(res => res.json());
  }
};

// components/UserList.vue
import api from '@/services/api';

export default {
  data() {
    return { users: [] };
  },
  
  async mounted() {
    this.users = await api.getUsers();
  }
};
```

#### **Angular:**
```typescript
// config.service.ts
import { Injectable } from '@angular/core';

@Injectable({
  providedIn: 'root'
})
export class ConfigService {
  private readonly API_BASE_URL = 'http://localhost:8080';
  
  get apiBaseUrl(): string {
    return this.API_BASE_URL;
  }
}

// user.service.ts
import { Injectable } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { ConfigService } from './config.service';

@Injectable({
  providedIn: 'root'
})
export class UserService {
  private readonly apiUrl = `${this.config.apiBaseUrl}/api/users`;
  
  constructor(
    private http: HttpClient,
    private config: ConfigService
  ) {}
  
  getUsers() {
    return this.http.get(this.apiUrl);
  }
  
  createUser(user: any) {
    return this.http.post(this.apiUrl, user);
  }
}
```

### **🔒 Security Configuration**

#### **API Key Authentication:**
```javascript
// Add API key to requests
const API_KEY = 'your-api-key-here';

const headers = {
  'Content-Type': 'application/json',
  'Authorization': `Bearer ${API_KEY}`,
  'X-API-Key': API_KEY
};

fetch('http://localhost:8080/api/users', {
  headers: headers
});
```

#### **JWT Authentication:**
```javascript
// JWT token handling
const token = localStorage.getItem('jwt_token');

const headers = {
  'Content-Type': 'application/json',
  'Authorization': `Bearer ${token}`
};

fetch('http://localhost:8080/api/users', {
  headers: headers
});
```

### **📊 Real-time Updates**

#### **WebSocket Connection:**
```javascript
// Connect to WebSocket for real-time updates
const ws = new WebSocket('ws://localhost:8081');

ws.onmessage = (event) => {
  const data = JSON.parse(event.data);
  console.log('Real-time update:', data);
  
  // Update your UI based on the data
  updateDashboard(data);
};

ws.onopen = () => {
  console.log('Connected to load balancer WebSocket');
};

ws.onerror = (error) => {
  console.error('WebSocket error:', error);
};
```

### **🧪 Testing Connection**

#### **Test API Endpoints:**
```bash
# Test health check
curl http://localhost:8080/health/live

# Test metrics
curl http://localhost:8080/metrics

# Test load balancer with sample request
curl -X POST http://localhost:8080/api/test \
  -H "Content-Type: application/json" \
  -d '{"message": "Hello from frontend"}'
```

#### **Browser Test:**
```html
<!DOCTYPE html>
<html>
<head>
    <title>Load Balancer Test</title>
</head>
<body>
    <h1>Load Balancer Connection Test</h1>
    <button onclick="testConnection()">Test Connection</button>
    <div id="result"></div>
    
    <script>
        async function testConnection() {
            try {
                const response = await fetch('http://localhost:8080/health/live');
                const data = await response.json();
                document.getElementById('result').innerHTML = 
                    `<h3>✅ Connected!</h3><pre>${JSON.stringify(data, null, 2)}</pre>`;
            } catch (error) {
                document.getElementById('result').innerHTML = 
                    `<h3>❌ Connection Failed</h3><p>${error.message}</p>`;
            }
        }
    </script>
</body>
</html>
```

### **🔧 Troubleshooting**

#### **Common Issues:**

1. **CORS Errors:**
   ```bash
   # Check if CORS is enabled in config
   grep -i cors config.json
   ```

2. **Connection Refused:**
   ```bash
   # Check if load balancer is running
   ps aux | grep loadbalancer
   
   # Check port availability
   lsof -i :8080
   ```

3. **Timeout Issues:**
   ```bash
   # Check backend servers are running
   curl http://localhost:8081/health
   curl http://localhost:8082/health
   curl http://localhost:8083/health
   ```

### **🚀 Production Deployment**

#### **Domain Configuration:**
```json
// Production config
{
  "host": "0.0.0.0",
  "port": 80,
  "domain": "your-domain.com",
  "tls": {
    "cert_file": "/path/to/cert.pem",
    "key_file": "/path/to/key.pem"
  }
}
```

#### **Frontend Production URL:**
```javascript
// Production frontend configuration
const config = {
  API_BASE_URL: 'https://your-domain.com',
  WEBSOCKET_URL: 'wss://your-domain.com:8081'
};
```

---

## 🎯 **Quick Start:**

1. **Load Balancer URL:** `http://localhost:8080`
2. **WebSocket URL:** `ws://localhost:8081`
3. **Metrics URL:** `http://localhost:9090`

**Your Enterprise Load Balancer is ready for frontend connections!** 🚀
