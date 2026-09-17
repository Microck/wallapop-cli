import type { BaseLayoutProps } from 'fumadocs-ui/layouts/shared';

// The mark is the brand blue on the page ground, no wordmark image, so the
// header stays legible at any size and needs no asset pipeline.
export const baseOptions: BaseLayoutProps = {
  nav: {
    title: (
      <span className="inline-flex items-center gap-2 font-semibold">
        <span
          aria-hidden
          className="inline-block size-3 rounded-[3px]"
          style={{ backgroundColor: '#385EF9' }}
        />
        wallapop-cli
      </span>
    ),
  },
  githubUrl: 'https://github.com/Microck/wallapop-cli',
};
