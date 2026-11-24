import starlight from '@astrojs/starlight';
import { nebari } from '@nebari/starlight';
import { defineConfig, passthroughImageService } from 'astro/config';

const section = (label, directory) => ({
  label,
  items: [{ autogenerate: { directory } }],
});

export default defineConfig({
  // Published by .github/workflows/docs.yml to GitHub Pages at this address.
  site: 'https://javo.nebari.dev',
  // The only images are SVGs, which need no processing (and no sharp).
  image: { service: passthroughImageService() },
  // Pages read files from the repository (the chart's values.yaml, the
  // OpenAPI document), one level above the site.
  vite: { server: { fs: { allow: ['..'] } } },
  integrations: [
    starlight({
      title: 'Tapas',
      description:
        'Tapas runs notebooks, web apps and scheduled jobs on Kubernetes, from one place.',
      favicon: '/brand/favicon.svg',
      editLink: { baseUrl: 'https://github.com/nebari-dev/tapas/edit/main/site/' },
      // Last-updated dates come from git history; off so builds of a shallow
      // clone or a tarball match.
      lastUpdated: false,
      // Tapas's own footer on the splash pages (the theme's names Nebari).
      components: { Footer: './src/components/Footer.astro' },
      customCss: ['./src/styles/custom.css'],
      plugins: [
        nebari({
          githubHref: 'https://github.com/nebari-dev/tapas',
          logo: {
            light: '/brand/tapas-logo.svg',
            dark: '/brand/tapas-logo-white.svg',
            alt: 'Tapas',
          },
          nav: [
            { label: 'Get started', href: '/getting-started/' },
            { label: 'Use', href: '/using/' },
            { label: 'Administer', href: '/admin/' },
            { label: 'Architecture', href: '/architecture/' },
            { label: 'Reference', href: '/reference/' },
            { label: 'Contribute', href: '/contributing/' },
          ],
        }),
      ],
      sidebar: [
        section('Get started', 'getting-started'),
        section('Use Tapas', 'using'),
        section('Administer', 'admin'),
        section('Architecture', 'architecture'),
        section('Reference', 'reference'),
        section('Contribute', 'contributing'),
      ],
    }),
  ],
});
