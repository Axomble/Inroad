// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// https://astro.build/config
export default defineConfig({
	integrations: [
		starlight({
			title: 'Inroad Documentation',
			description: 'Self-hostable cold email sequencing & mailbox warmup platform. Open-core alternative to Instantly and Smartlead.',
			logo: {
				src: './src/assets/logo.png',
				replacesTitle: false,
			},
			social: [
				{ icon: 'github', label: 'GitHub', href: 'https://github.com/inroad/inroad' },
			],
			sidebar: [
				{
					label: 'Overview & Architecture',
					items: [
						{ label: 'System Architecture & Planes', slug: 'architecture' },
						{ label: 'Core Security Invariants', slug: 'security' },
					],
				},
				{
					label: 'User Guides & Features',
					items: [
						{ label: 'Cold Campaigns & Cadences', slug: 'guides/campaigns' },
						{ label: 'Mailbox Connections & Security', slug: 'guides/mailboxes' },
						{ label: 'Automated Mailbox Warmup', slug: 'guides/warmup' },
						{ label: 'CRM & Contact Management', slug: 'guides/crm-contacts' },
						{ label: 'Deliverability & Circuit Breaker', slug: 'guides/deliverability' },
						{ label: 'Offline Reply Classification', slug: 'guides/reply-classification' },
						{ label: 'Unified Inbox', slug: 'guides/unified-inbox' },
						{ label: 'Authentication & Tenant Security', slug: 'guides/auth-security' },
					],
				},
				{
					label: 'Self-Hosting & Deployments',
					items: [
						{ label: 'Single-Instance Docker Compose', slug: 'deploy/docker-compose' },
						{ label: 'AWS Production (Terraform)', slug: 'deploy/aws-terraform' },
						{ label: 'Kubernetes Cluster (Helm)', slug: 'deploy/kubernetes-helm' },
						{ label: 'Environment Variables Reference', slug: 'deploy/environment-variables' },
					],
				},
				{
					label: 'MCP Server & AI Integrations',
					items: [
						{ label: 'MCP Server Overview (/v1/mcp)', slug: 'mcp' },
						{ label: 'Claude Desktop Integration', slug: 'mcp/claude-desktop' },
						{ label: 'Cursor & Windsurf Setup', slug: 'mcp/cursor-windsurf' },
						{ label: 'LangChain & Python/TS SDKs', slug: 'mcp/langchain-sdks' },
						{ label: 'MCP Tool Registry Reference', slug: 'mcp/tool-reference' },
					],
				},
			],
		}),
	],
});
