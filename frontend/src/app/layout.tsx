import type { Metadata } from 'next';
import './globals.css';

export const metadata: Metadata = {
  title: 'TrialTerminator — Cancel what\'s draining your card',
  description: 'AI-powered subscription cancellation. Enter a URL, we handle the rest.',
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body className="min-h-screen antialiased text-gray-900">{children}</body>
    </html>
  );
}
