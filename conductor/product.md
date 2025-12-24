# Product Guide - Karpetrack

## Initial Concept
A Kubernetes controller for managing Rackspace Spot instances with automatic cost optimization, inspired by [Karpenter](https://karpenter.sh/).

## Target Users
- **DevOps teams** looking to reduce cloud infrastructure costs automatically.
- **Individual developers** running hobbyist Kubernetes projects on Rackspace Spot.

## Product Goals
- **Seamless Experience:** Provide a "Karpenter-like" experience for Rackspace Spot users, making spot instance management intuitive and automated.
- **Cost Efficiency:** Maintain optimal cluster sizing and instance selection to minimize spend without manual intervention.

## Core Features
- **Complex Scheduling Support:** Full support for Kubernetes scheduling constraints including Taints, Tolerations, and Node/Pod Affinities to ensure workloads land on the right spot instances.
- **Automatic Node Provisioning:** Dynamic response to unschedulable pods by provisioning the most cost-effective spot instances that meet workload requirements.
- **Cost Optimization:** Continuous background monitoring of spot prices with automated replacement of nodes when cheaper alternatives become available.

## Success Metrics
- **Cost Savings:** Percentage of total infrastructure cost saved compared to standard on-demand instance pricing.
