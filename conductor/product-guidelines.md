# Product Guidelines - Karpetrack

## Design Principles
- **Minimalist & Efficient Communication:** Documentation and user-facing messages should be brief, direct, and focused on helping users achieve value quickly without unnecessary fluff.
- **Transparent Automation:** Automated actions like node replacement and consolidation must be clearly communicated through detailed, structured logging (the "why") and Kubernetes Events (the "what") for easy auditing and observability.
- **Balanced Defaults:** Default configurations should strike a 50/50 balance between stability and cost optimization—aggressive enough to provide immediate value but conservative enough to maintain high availability.
- **Intelligent Robustness:** Automation should handle transient failures (API timeouts, provisioning issues) gracefully using exponential backoff and alternate strategy retries before requiring manual intervention.

## User Experience Goals
- **Self-Service Observability:** Users should be able to understand the controller's decision-making process by simply looking at logs or events.
- **Configuration Clarity:** Configuration options should be intuitive, with clear impacts on the balance between cost and stability.
