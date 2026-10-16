import { defineEnvVars } from '@sveltejs/kit/env';

export const variables = defineEnvVars({
	BACKEND_URL: {
		description: 'URL of the Go drop server',
		schema: (value) => value
	}
});
