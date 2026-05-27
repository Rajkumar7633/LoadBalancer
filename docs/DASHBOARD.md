# 🎯 Real-Time Dashboard Guide

## Overview

The Enterprise Load Balancer includes a comprehensive real-time dashboard that provides live visualization of system metrics, security events, auto-scaling status, and backend health. The dashboard uses WebSocket technology for real-time data streaming and provides an intuitive interface for monitoring and managing your load balancer.

## 🚀 Quick Start

### Method 1: Docker Compose (Recommended)

```bash
# Start the complete dashboard stack
docker-compose -f docker-compose.dashboard.yml up -d

# Access the dashboard
open http://localhost:8081

# Access Grafana (optional)
open http://localhost:3000
# Username: admin, Password: admin

# Access Prometheus (optional)
open http://localhost:9090
```

### Method 2: Manual Build

```bash
# Build the dashboard
go build -o dashboard ./cmd/dashboard

# Run the dashboard
./dashboard --addr=:8081

# Open in browser
open http://localhost:8081
```

## 📊 Dashboard Features

### 1. **Real-Time Metrics**
- **Requests Per Second**: Live RPS monitoring with trend indicators
- **Active Connections**: Current connection count with percentage changes
- **Average Response Time**: Response time metrics in milliseconds
- **Error Rate**: Percentage of failed requests with trend analysis

### 2. **Interactive Charts**
- **Request Rate Chart**: Real-time line chart showing RPS over time
- **Response Time Chart**: Live visualization of response time trends
- **Auto-updating**: Charts refresh every 2 seconds via WebSocket

### 3. **Backend Status**
- **Health Monitoring**: Real-time status of all backend servers
- **Connection Count**: Active connections per backend
- **Response Time**: Individual backend performance metrics
- **Visual Indicators**: Color-coded status (green=healthy, red=unhealthy)

### 4. **Security Events**
- **Live Security Feed**: Real-time security event notifications
- **Event Types**: WAF blocks, rate limits, authentication failures
- **Severity Levels**: High, medium, low priority indicators
- **Detailed Information**: Client IP, user agent, timestamps

### 5. **Auto-Scaling Visualization**
- **Current Scale**: Active number of backend instances
- **Resource Usage**: CPU and memory utilization bars
- **Scale Range**: Minimum and maximum scaling limits
- **Last Event**: Most recent scaling action and timing

### 6. **Top Endpoints**
- **Traffic Analysis**: Most accessed API endpoints
- **Performance Metrics**: Request count and average response time
- **Error Rates**: Per-endpoint error percentages
- **Real-time Updates**: Live ranking of endpoint usage

## 🎨 Dashboard Interface

### Header Section
- **Connection Status**: Real-time WebSocket connection indicator
- **Current Time**: Live clock display
- **Fullscreen Mode**: Toggle fullscreen viewing

### Key Metrics Cards
- **Animated Values**: Smooth transitions for metric changes
- **Trend Indicators**: Up/down arrows with percentage changes
- **Color Coding**: Blue (RPS), Green (connections), Yellow (response time), Red (errors)

### Chart Controls
- **Responsive Design**: Charts adapt to screen size
- **Interactive Tooltips**: Hover for detailed values
- **Time Window**: Last 20 data points (40 seconds of history)

## 📱 Mobile Responsive

The dashboard is fully responsive and works on:
- **Desktop**: Full-featured experience with all charts
- **Tablet**: Optimized layout with touch interactions
- **Mobile**: Compact view with essential metrics

## 🔧 Configuration Options

### Environment Variables
```bash
# Server configuration
export DASHBOARD_ADDR=:8081
export LOG_LEVEL=info
export GO_ENV=production

# WebSocket settings
export WS_HEARTBEAT_INTERVAL=30s
export WS_WRITE_TIMEOUT=10s
export WS_READ_TIMEOUT=60s
```

### Customization
- **Update Frequency**: Modify broadcast interval in `server.go`
- **Chart History**: Adjust data point retention
- **Theme Colors**: Customize CSS variables in `dashboard.html`
- **Metrics**: Add or remove tracked metrics

## 🔌 WebSocket API

### Connection
```javascript
const ws = new WebSocket('ws://localhost:8081/ws');
```

### Message Format
```json
{
  "timestamp": "2024-01-01T12:00:00Z",
  "metrics": {
    "requests_per_second": 1250,
    "active_connections": 342,
    "avg_response_time": 25,
    "error_rate": 0.5,
    "cpu_usage": 45.2,
    "memory_usage": 67.8
  },
  "backends": [
    {
      "id": "backend-1",
      "url": "http://localhost:8081",
      "status": "healthy",
      "connections": 120,
      "response_time": 25,
      "request_count": 10000,
      "error_count": 50
    }
  ],
  "security_events": [
    {
      "type": "WAF_BLOCK",
      "message": "SQL injection attempt blocked",
      "severity": "high",
      "timestamp": "2024-01-01T12:00:00Z",
      "client_ip": "192.168.1.100",
      "user_agent": "Mozilla/5.0..."
    }
  ],
  "autoscaling": {
    "current_scale": 3,
    "min_scale": 2,
    "max_scale": 10,
    "cpu_usage": 45.2,
    "memory_usage": 67.8,
    "last_event": "scale_up",
    "last_event_time": "2024-01-01T11:58:00Z"
  },
  "top_endpoints": [
    {
      "path": "/api/users",
      "requests": 1250,
      "avg_response_time": 22,
      "error_rate": 0.5,
      "last_minute_rps": 20
    }
  ]
}
```

## 🔍 Troubleshooting

### Common Issues

#### Dashboard Not Loading
```bash
# Check if port is available
netstat -tulpn | grep 8081

# Check logs
docker-compose -f docker-compose.dashboard.yml logs dashboard
```

#### WebSocket Connection Failed
```bash
# Verify WebSocket endpoint
curl -i -N -H "Connection: Upgrade" \
     -H "Upgrade: websocket" \
     -H "Sec-WebSocket-Key: test" \
     -H "Sec-WebSocket-Version: 13" \
     http://localhost:8081/ws
```

#### No Real-Time Updates
```bash
# Check browser console for WebSocket errors
# Verify network connectivity
# Check server logs for connection errors
```

### Performance Optimization

#### For High Traffic
- Increase WebSocket buffer sizes
- Implement connection pooling
- Use CDN for static assets
- Enable gzip compression

#### For Large Deployments
- Deploy multiple dashboard instances
- Use Redis for session sharing
- Implement horizontal scaling
- Add load balancing for dashboard

## 🔒 Security Considerations

### Production Deployment
- **HTTPS**: Enable TLS for dashboard access
- **Authentication**: Add login/authentication
- **CORS**: Configure proper cross-origin settings
- **Rate Limiting**: Protect against dashboard abuse

### Network Security
```bash
# Firewall rules
ufw allow 8081/tcp  # Dashboard
ufw allow 9090/tcp  # Prometheus
ufw allow 3000/tcp  # Grafana
```

## 📈 Integration with Monitoring Stack

### Prometheus Integration
The dashboard automatically exposes metrics for Prometheus:
- `dashboard_websocket_connections_total`
- `dashboard_requests_total`
- `dashboard_message_broadcast_duration_seconds`

### Grafana Dashboards
Import pre-built dashboards:
1. Navigate to Grafana → Import
2. Use dashboard ID: `12345` (Load Balancer Dashboard)
3. Select Prometheus datasource

### Alerting
Set up alerts for:
- High error rates (>5%)
- Backend failures
- WebSocket connection issues
- Resource utilization thresholds

## 🎯 Best Practices

### Monitoring
- **Check Regularly**: Monitor dashboard for anomalies
- **Set Alerts**: Configure notifications for critical metrics
- **Review Logs**: Analyze security events daily
- **Performance Tuning**: Optimize based on dashboard insights

### Maintenance
- **Regular Updates**: Keep dashboard components updated
- **Backup Configuration**: Save custom dashboard settings
- **Monitor Resources**: Ensure adequate server resources
- **Security Audits**: Regular security assessments

## 🚀 Advanced Features

### Custom Metrics
Add your own metrics to the dashboard:
```go
// In server.go
func (ws *WebSocketServer) generateCustomMetrics() map[string]interface{} {
    return map[string]interface{}{
        "custom_metric_1": getValue(),
        "custom_metric_2": getValue(),
    }
}
```

### Third-Party Integrations
- **Slack Notifications**: Send alerts to Slack
- **PagerDuty**: Integrate with incident management
- **DataDog**: Export metrics to DataDog
- **ELK Stack**: Forward logs to Elasticsearch

### API Extensions
Create custom API endpoints:
```go
mux.HandleFunc("/api/custom", customHandler)
mux.HandleFunc("/api/health", healthHandler)
```

## 📞 Support

For dashboard-related issues:
1. Check the troubleshooting section
2. Review browser console logs
3. Verify server logs
4. Create an issue with detailed information
5. Include screenshots and error messages

---

**The real-time dashboard provides complete visibility into your load balancer operations, enabling proactive monitoring and quick issue resolution.** 🎯
