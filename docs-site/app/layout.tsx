import './global.css';
import { RootProvider } from 'fumadocs-ui/provider/next';
import type { ReactNode } from 'react';
import type { Metadata } from 'next';

export const metadata: Metadata = {
  title: { default: 'wallapop-cli', template: '%s | wallapop-cli' },
  description: 'Drive Wallapop from the terminal: search, listings, chat and watches, with JSON output.',
};

export default function Layout({ children }: { children: ReactNode }) {
  return (
    <html lang="en" className="dark" suppressHydrationWarning>
      <body className="flex min-h-screen flex-col">
        <RootProvider theme={{ defaultTheme: 'dark' }}>{children}</RootProvider>
      </body>
    </html>
  );
}
